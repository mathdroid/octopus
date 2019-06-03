package truapi

import (
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"time"

	"github.com/TruStory/octopus/services/truapi/chttp"
	truCtx "github.com/TruStory/octopus/services/truapi/context"
	"github.com/TruStory/octopus/services/truapi/db"
	"github.com/TruStory/octopus/services/truapi/graphql"
	"github.com/TruStory/octopus/services/truapi/truapi/cookies"
	app "github.com/TruStory/truchain/types"
	"github.com/TruStory/truchain/x/argument"
	"github.com/TruStory/truchain/x/backing"
	"github.com/TruStory/truchain/x/category"
	"github.com/TruStory/truchain/x/challenge"
	"github.com/TruStory/truchain/x/params"
	"github.com/TruStory/truchain/x/story"
	trubank "github.com/TruStory/truchain/x/trubank"
	"github.com/TruStory/truchain/x/users"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/dghubble/gologin/twitter"
	"github.com/dghubble/oauth1"
	twitterOAuth1 "github.com/dghubble/oauth1/twitter"
	"github.com/gorilla/handlers"
	"github.com/julianshen/og"
)

// ContextKey represents a string key for request context.
type ContextKey string

const (
	userContextKey = ContextKey("user")
)

// TruAPI implements an HTTP server for TruStory functionality using `chttp.API`
type TruAPI struct {
	*chttp.API
	APIContext    truCtx.TruAPIContext
	GraphQLClient *graphql.Client
	DBClient      db.Datastore

	// notifications
	notificationsInitialized bool
	commentsNotificationsCh  chan CommentNotificationRequest
	httpClient               *http.Client
}

// NewTruAPI returns a `TruAPI` instance populated with the existing app and a new GraphQL client
func NewTruAPI(apiCtx truCtx.TruAPIContext) *TruAPI {
	ta := TruAPI{
		API:                     chttp.NewAPI(apiCtx, supported),
		APIContext:              apiCtx,
		GraphQLClient:           graphql.NewGraphQLClient(),
		DBClient:                db.NewDBClient(apiCtx.Config),
		commentsNotificationsCh: make(chan CommentNotificationRequest),
		httpClient: &http.Client{
			Timeout: time.Second * 5,
		},
	}

	return &ta
}

// RunNotificationSender connects to the push notification service
func (ta *TruAPI) RunNotificationSender(apiCtx truCtx.TruAPIContext) error {
	ta.notificationsInitialized = true
	go ta.runCommentNotificationSender(ta.commentsNotificationsCh, apiCtx.Config.Push.EndpointURL)
	return nil
}

// WrapHandler wraps a chttp.Handler and returns a standar http.Handler
func WrapHandler(h chttp.Handler) http.Handler {
	return h.HandlerFunc()
}

// WithUser sets the user in the context that will be passed down to handlers.
func WithUser(apiCtx truCtx.TruAPIContext, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, err := cookies.GetAuthenticatedUser(apiCtx, r)
		if err != nil {
			h.ServeHTTP(w, r)
			return
		}
		ctx := context.WithValue(r.Context(), userContextKey, auth)
		h.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RegisterRoutes applies the TruStory API routes to the `chttp.API` router
func (ta *TruAPI) RegisterRoutes(apiCtx truCtx.TruAPIContext) {
	api := ta.Subrouter("/api/v1")

	// Enable gzip compression
	api.Use(handlers.CompressHandler)
	api.Use(chttp.JSONResponseMiddleware)
	api.Handle("/ping", WrapHandler(ta.HandlePing))
	api.Handle("/graphql", WithUser(apiCtx, WrapHandler(ta.HandleGraphQL)))
	api.Handle("/presigned", WrapHandler(ta.HandlePresigned))
	api.Handle("/unsigned", WrapHandler(ta.HandleUnsigned))
	api.Handle("/register", WrapHandler(ta.HandleRegistration))
	api.Handle("/user", WrapHandler(ta.HandleUserDetails))
	api.Handle("/user/search", WrapHandler(ta.HandleUsernameSearch))
	api.Handle("/notification", WithUser(apiCtx, WrapHandler(ta.HandleNotificationEvent)))
	api.HandleFunc("/deviceToken", ta.HandleDeviceTokenRegistration)
	api.HandleFunc("/deviceToken/unregister", ta.HandleUnregisterDeviceToken)
	api.HandleFunc("/upload", ta.HandleUpload)
	api.Handle("/flagStory", WithUser(apiCtx, WrapHandler(ta.HandleFlagStory)))
	api.Handle("/comments", WithUser(apiCtx, WrapHandler(ta.HandleComment)))
	api.Handle("/invite", WithUser(apiCtx, WrapHandler(ta.HandleInvite)))
	api.Handle("/reactions", WithUser(apiCtx, WrapHandler(ta.HandleReaction)))
	api.HandleFunc("/mentions/translateToCosmos", ta.HandleTranslateCosmosMentions)
	api.HandleFunc("/metrics", ta.HandleMetrics)
	api.Handle("/track/", WithUser(apiCtx, http.HandlerFunc(ta.HandleTrackEvent)))

	if apiCtx.Config.App.MockRegistration {
		api.Handle("/mock_register", WrapHandler(ta.HandleMockRegistration))
	}

	ta.RegisterOAuthRoutes(apiCtx)

	// Register routes for Trustory React web app

	fs := http.FileServer(http.Dir(apiCtx.Config.Web.Directory))
	fsV2 := http.FileServer(http.Dir(apiCtx.Config.Web.DirectoryV2))

	ta.PathPrefix("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		webVersionCookie, _ := r.Cookie("web_version")
		webDirectory := apiCtx.Config.Web.Directory
		if webVersionCookie != nil && webVersionCookie.Value == "2" {
			webDirectory = apiCtx.Config.Web.DirectoryV2
		}
		// if it is not requesting a file with a valid extension serve the index
		if filepath.Ext(path.Base(r.URL.Path)) == "" {
			indexPath := filepath.Join(webDirectory, "index.html")

			absIndexPath, err := filepath.Abs(indexPath)
			if err != nil {
				log.Printf("ERROR index.html -- %s", err)
				http.Error(w, "Error serving index.html", http.StatusNotFound)
				return
			}
			indexFile, err := ioutil.ReadFile(absIndexPath)
			if err != nil {
				log.Printf("ERROR index.html -- %s", err)
				http.Error(w, "Error serving index.html", http.StatusNotFound)
				return
			}
			compiledIndexFile := CompileIndexFile(ta, indexFile, r.RequestURI)

			w.Header().Add("Content-Type", "text/html")
			_, err = fmt.Fprint(w, compiledIndexFile)
			if err != nil {
				log.Printf("ERROR index.html -- %s", err)
				http.Error(w, "Error serving index.html", http.StatusInternalServerError)
				return
			}
			return
		}
		if webVersionCookie != nil && webVersionCookie.Value == "2" {
			fsV2.ServeHTTP(w, r)
			return
		}
		fs.ServeHTTP(w, r)
	}))
}

// RegisterOAuthRoutes adds the proper routes needed for the oauth
func (ta *TruAPI) RegisterOAuthRoutes(apiCtx truCtx.TruAPIContext) {
	oauth1Config := &oauth1.Config{
		ConsumerKey:    apiCtx.Config.Twitter.APIKey,
		ConsumerSecret: apiCtx.Config.Twitter.APISecret,
		CallbackURL:    apiCtx.Config.Twitter.OAUTHCallback,
		Endpoint:       twitterOAuth1.AuthorizeEndpoint,
	}

	ta.Handle("/auth-twitter", twitter.LoginHandler(oauth1Config, nil))
	ta.Handle("/auth-twitter-callback", HandleOAuthSuccess(oauth1Config, IssueSession(apiCtx, ta), HandleOAuthFailure(ta)))
	ta.Handle("/auth-logout", Logout(apiCtx))
}

// RegisterMutations registers mutations
func (ta *TruAPI) RegisterMutations() {
	ta.GraphQLClient.RegisterMutation("addComment", func(args struct {
		Parent int64
		Body   string
	}) error {
		err := ta.DBClient.AddComment(&db.Comment{ParentID: args.Parent, Body: args.Body})
		return err
	})
}

// RegisterResolvers builds the app's GraphQL schema from resolvers (declared in `resolver.go`)
func (ta *TruAPI) RegisterResolvers() {
	formatTime := func(t time.Time) string {
		return t.UTC().Format(time.UnixDate)
	}

	getUser := func(ctx context.Context, addr sdk.AccAddress) users.User {
		res := ta.usersResolver(ctx, users.QueryUsersByAddressesParams{Addresses: []string{addr.String()}})
		if len(res) > 0 {
			return res[0]
		}
		return users.User{}
	}

	getBackings := func(ctx context.Context, storyID int64) []backing.Backing {
		return ta.backingsResolver(ctx, app.QueryByIDParams{ID: storyID})
	}

	getChallenges := func(ctx context.Context, storyID int64) []challenge.Challenge {
		return ta.challengesResolver(ctx, app.QueryByIDParams{ID: storyID})
	}

	getTransactions := func(ctx context.Context, creator string) []trubank.Transaction {
		return ta.transactionsResolver(ctx, app.QueryByCreatorParams{Creator: creator})
	}

	getStory := func(ctx context.Context, storyID int64) story.Story {
		return ta.storyResolver(ctx, story.QueryStoryByIDParams{ID: storyID})
	}

	getArgument := func(ctx context.Context, argumentID int64, raw bool) argument.Argument {
		return ta.argumentResolver(ctx, app.QueryArgumentByID{ID: argumentID, Raw: raw})
	}

	ta.GraphQLClient.RegisterQueryResolver("comments", ta.commentsResolver)
	ta.GraphQLClient.RegisterObjectResolver("Comment", db.Comment{}, map[string]interface{}{
		"id":         func(_ context.Context, q db.Comment) int64 { return q.ID },
		"parentId":   func(_ context.Context, q db.Comment) int64 { return q.ParentID },
		"argumentId": func(_ context.Context, q db.Comment) int64 { return q.ArgumentID },
		"body":       func(_ context.Context, q db.Comment) string { return q.Body },
		"creator": func(ctx context.Context, q db.Comment) users.User {
			creator, err := sdk.AccAddressFromBech32(q.Creator)
			if err != nil {
				// [shanev] TODO: handle error better, see https://github.com/TruStory/truchain/issues/199
				panic(err)
			}
			return getUser(ctx, creator)
		},
		"createdAt": func(_ context.Context, q db.Comment) time.Time { return q.CreatedAt },
	})

	ta.GraphQLClient.RegisterQueryResolver("argument", ta.argumentResolver)
	ta.GraphQLClient.RegisterObjectResolver("Argument", argument.Argument{}, map[string]interface{}{
		"id":      func(_ context.Context, q argument.Argument) int64 { return q.ID },
		"creator": func(ctx context.Context, q argument.Argument) users.User { return getUser(ctx, q.Creator) },
		"body":    func(_ context.Context, q argument.Argument) string { return q.Body },
		"storyId": func(_ context.Context, q argument.Argument) int64 { return q.StoryID },
		"likes": func(ctx context.Context, q argument.Argument) []argument.Like {
			return ta.likesObjectResolver(ctx, app.QueryByIDParams{ID: q.ID})
		},
		"reactionsCount": func(ctx context.Context, q argument.Argument) []db.ReactionsCount {
			rxnable := db.Reactionable{
				Type: "arguments",
				ID:   q.ID,
			}
			return ta.reactionsCountResolver(ctx, rxnable)
		},
		"reactions": func(ctx context.Context, q argument.Argument) []db.Reaction {
			rxnable := db.Reactionable{
				Type: "arguments",
				ID:   q.ID,
			}
			return ta.reactionsResolver(ctx, rxnable)
		},
		"timestamp": func(_ context.Context, q argument.Argument) app.Timestamp { return q.Timestamp },
		"comments":  ta.commentsResolver,
	})

	ta.GraphQLClient.RegisterObjectResolver("Reaction", db.Reaction{}, map[string]interface{}{
		"id":   func(_ context.Context, q db.Reaction) int64 { return q.ID },
		"type": func(_ context.Context, q db.Reaction) db.ReactionType { return q.ReactionType },
		"creator": func(ctx context.Context, q db.Reaction) users.User {
			creator, err := sdk.AccAddressFromBech32(q.Creator)
			if err != nil {
				// [shanev] TODO: handle error better, see https://github.com/TruStory/truchain/issues/199
				panic(err)
			}
			return getUser(ctx, creator)
		},
	})

	ta.GraphQLClient.RegisterObjectResolver("Like", argument.Like{}, map[string]interface{}{
		"argumentId": func(_ context.Context, q argument.Like) int64 { return q.ArgumentID },
		"creator":    func(ctx context.Context, q argument.Like) users.User { return getUser(ctx, q.Creator) },
		"timestamp":  func(_ context.Context, q argument.Like) app.Timestamp { return q.Timestamp },
	})

	ta.GraphQLClient.RegisterQueryResolver("backing", ta.backingResolver)
	ta.GraphQLClient.RegisterObjectResolver("Backing", backing.Backing{}, map[string]interface{}{
		"amount": func(ctx context.Context, q backing.Backing) sdk.Coin { return q.Amount() },
		"argument": func(ctx context.Context, q backing.Backing, args struct {
			Raw bool `graphql:",optional"`
		}) argument.Argument {
			return getArgument(ctx, q.ArgumentID, args.Raw)
		},
		"vote":      func(ctx context.Context, q backing.Backing) bool { return q.VoteChoice() },
		"creator":   func(ctx context.Context, q backing.Backing) users.User { return getUser(ctx, q.Creator()) },
		"timestamp": func(ctx context.Context, q backing.Backing) app.Timestamp { return q.Timestamp() },

		// Deprecated: interest is no longer saved in backing
		"interest": func(ctx context.Context, q backing.Backing) sdk.Coin { return sdk.Coin{} },
	})

	ta.GraphQLClient.RegisterQueryResolver("categories", ta.allCategoriesResolver)
	ta.GraphQLClient.RegisterQueryResolver("category", ta.categoryResolver)
	ta.GraphQLClient.RegisterObjectResolver("Category", category.Category{}, map[string]interface{}{
		"id": func(_ context.Context, q category.Category) int64 { return q.ID },
	})

	ta.GraphQLClient.RegisterQueryResolver("challenge", ta.challengeResolver)
	ta.GraphQLClient.RegisterObjectResolver("Challenge", challenge.Challenge{}, map[string]interface{}{
		"amount": func(ctx context.Context, q challenge.Challenge) sdk.Coin { return q.Amount() },
		"argument": func(ctx context.Context, q challenge.Challenge, args struct {
			Raw bool `graphql:",optional"`
		}) argument.Argument {
			return getArgument(ctx, q.ArgumentID, args.Raw)
		},
		"vote":      func(ctx context.Context, q challenge.Challenge) bool { return q.VoteChoice() },
		"creator":   func(ctx context.Context, q challenge.Challenge) users.User { return getUser(ctx, q.Creator()) },
		"timestamp": func(ctx context.Context, q challenge.Challenge) app.Timestamp { return q.Timestamp() },
	})

	ta.GraphQLClient.RegisterObjectResolver("Coin", sdk.Coin{}, map[string]interface{}{
		"amount":        func(_ context.Context, q sdk.Coin) string { return q.Amount.String() },
		"denom":         func(_ context.Context, q sdk.Coin) string { return q.Denom },
		"humanReadable": func(_ context.Context, q sdk.Coin) string { return HumanReadable(q) },
	})

	ta.GraphQLClient.RegisterQueryResolver("invites", ta.invitesResolver)
	ta.GraphQLClient.RegisterObjectResolver("Invite", db.Invite{}, map[string]interface{}{
		"id": func(_ context.Context, i db.Invite) int64 { return i.ID },
		"creator": func(ctx context.Context, i db.Invite) users.User {
			creator, err := sdk.AccAddressFromBech32(i.Creator)
			if err != nil {
				return users.User{}
			}
			return getUser(ctx, creator)
		},
		"friend": func(ctx context.Context, i db.Invite) users.User {
			twitterProfile, err := ta.DBClient.TwitterProfileByUsername(i.FriendTwitterUsername)
			if err != nil || twitterProfile == nil {
				return users.User{}
			}
			friend, err := sdk.AccAddressFromBech32(twitterProfile.Address)
			if err != nil {
				return users.User{}
			}
			return getUser(ctx, friend)
		},
		"createdAt": func(_ context.Context, q db.Invite) time.Time { return q.CreatedAt },
	})

	ta.GraphQLClient.RegisterQueryResolver("params", ta.paramsResolver)
	ta.GraphQLClient.RegisterObjectResolver("Params", params.Params{}, map[string]interface{}{
		"interestRate":     func(_ context.Context, p params.Params) string { return p.StakeParams.InterestRate.String() },
		"maxStakeAmount":   func(_ context.Context, p params.Params) string { return p.StakeParams.MaxAmount.Amount.String() },
		"stakeToCredRatio": func(_ context.Context, p params.Params) string { return p.StakeParams.StakeToCredRatio.String() },

		"minArgumentLength": func(_ context.Context, p params.Params) int { return p.ArgumentParams.MinArgumentLength },
		"maxArgumentLength": func(_ context.Context, p params.Params) int { return p.ArgumentParams.MaxArgumentLength },

		"storyExpireDuration": func(_ context.Context, p params.Params) string {
			return fmt.Sprintf("%d", p.StoryParams.ExpireDuration)
		},
		"claimMinLength": func(_ context.Context, p params.Params) int { return p.StoryParams.MinStoryLength },
		"claimMaxLength": func(_ context.Context, p params.Params) int { return p.StoryParams.MaxStoryLength },

		"challengeMinStake": func(_ context.Context, p params.Params) string { return p.ChallengeParams.MinChallengeStake.String() },
		"stakeDenom":        func(_ context.Context, _ params.Params) string { return app.StakeDenom },

		// Deprecated
		"amountWeight":    func(_ context.Context, p params.Params) string { return "0" },
		"periodWeight":    func(_ context.Context, p params.Params) string { return "0" },
		"minInterestRate": func(_ context.Context, p params.Params) string { return "0" },
		"maxInterestRate": func(_ context.Context, p params.Params) string { return "0" },
	})

	ta.GraphQLClient.RegisterPaginatedQueryResolverWithFilter("paginated_stories", ta.storiesResolver, map[string]interface{}{
		"body": func(_ context.Context, q story.Story) string { return q.Body },
	})

	ta.GraphQLClient.RegisterQueryResolver("story", ta.storyResolver)
	ta.GraphQLClient.RegisterPaginatedObjectResolver("Story", "iD", story.Story{}, map[string]interface{}{
		"id":                  func(_ context.Context, q story.Story) int64 { return q.ID },
		"backings":            func(ctx context.Context, q story.Story) []backing.Backing { return getBackings(ctx, q.ID) },
		"challenges":          func(ctx context.Context, q story.Story) []challenge.Challenge { return getChallenges(ctx, q.ID) },
		"backingPool":         ta.backingPoolResolver,
		"challengePool":       ta.challengePoolResolver,
		"category":            ta.storyCategoryResolver,
		"creator":             func(ctx context.Context, q story.Story) users.User { return getUser(ctx, q.Creator) },
		"source":              func(ctx context.Context, q story.Story) string { return q.Source.String() },
		"state":               func(ctx context.Context, q story.Story) story.Status { return q.Status },
		"expireTime":          func(_ context.Context, q story.Story) string { return formatTime(q.ExpireTime) },
		"votingStartTime":     func(_ context.Context, q story.Story) string { return formatTime(q.VotingStartTime) },
		"votingEndTime":       func(_ context.Context, q story.Story) string { return formatTime(q.VotingEndTime) },
		"addressesWhoFlagged": ta.addressesWhoFlaggedResolver,
	})

	ta.GraphQLClient.RegisterObjectResolver("Timestamp", app.Timestamp{}, map[string]interface{}{
		"createdTime": func(_ context.Context, t app.Timestamp) string { return formatTime(t.CreatedTime) },
		"updatedTime": func(_ context.Context, t app.Timestamp) string { return formatTime(t.UpdatedTime) },
	})

	ta.GraphQLClient.RegisterObjectResolver("TwitterProfile", db.TwitterProfile{}, map[string]interface{}{
		"id": func(_ context.Context, q db.TwitterProfile) string { return string(q.ID) },
	})

	ta.GraphQLClient.RegisterQueryResolver("users", ta.usersResolver)
	ta.GraphQLClient.RegisterObjectResolver("User", users.User{}, map[string]interface{}{
		"id":     func(_ context.Context, q users.User) string { return q.Address },
		"coins":  func(_ context.Context, q users.User) sdk.Coins { return q.Coins },
		"pubkey": func(_ context.Context, q users.User) string { return q.Pubkey.String() },
		"twitterProfile": func(ctx context.Context, q users.User) db.TwitterProfile {
			return ta.twitterProfileResolver(ctx, q.Address)
		},
		"transactions": func(ctx context.Context, q users.User) []trubank.Transaction {
			return getTransactions(ctx, q.Address)
		},
	})

	ta.GraphQLClient.RegisterObjectResolver("Transactions", trubank.Transaction{}, map[string]interface{}{
		"id":              func(_ context.Context, q trubank.Transaction) int64 { return q.ID },
		"transactionType": func(_ context.Context, q trubank.Transaction) trubank.TransactionType { return q.TransactionType },
		"amount":          func(_ context.Context, q trubank.Transaction) sdk.Coin { return q.Amount },
		"createdTime":     func(_ context.Context, q trubank.Transaction) time.Time { return q.Timestamp.CreatedTime },
		"story": func(ctx context.Context, q trubank.Transaction) story.Story {
			return getStory(ctx, q.GroupID)
		},
	})

	ta.GraphQLClient.RegisterObjectResolver("URL", url.URL{}, map[string]interface{}{
		"url": func(_ context.Context, q url.URL) string { return q.String() },
	})

	ta.GraphQLClient.RegisterQueryResolver("notifications", ta.notificationsResolver)
	ta.GraphQLClient.RegisterObjectResolver("NotificationMeta", db.NotificationMeta{}, map[string]interface{}{})
	ta.GraphQLClient.RegisterObjectResolver("NotificationEvent", db.NotificationEvent{}, map[string]interface{}{
		"id": func(_ context.Context, q db.NotificationEvent) int64 { return q.ID },
		"userId": func(_ context.Context, q db.NotificationEvent) int64 {
			if q.SenderProfile != nil {
				return q.SenderProfileID
			}
			return q.TwitterProfileID
		},
		"title": func(_ context.Context, q db.NotificationEvent) string {
			if q.SenderProfile != nil {
				return q.SenderProfile.Username
			}
			return "Story Update"
		},
		"createdTime": func(_ context.Context, q db.NotificationEvent) time.Time {
			return q.Timestamp
		},
		"body": func(_ context.Context, q db.NotificationEvent) string {
			return q.Message
		},
		"typeId": func(_ context.Context, q db.NotificationEvent) int64 { return q.TypeID },
		"image": func(_ context.Context, q db.NotificationEvent) string {
			if q.SenderProfile != nil {
				return q.SenderProfile.AvatarURI
			}
			return q.TwitterProfile.AvatarURI
		},
		"meta": func(_ context.Context, q db.NotificationEvent) db.NotificationMeta {
			return q.Meta
		},
	})

	ta.GraphQLClient.RegisterQueryResolver("unreadNotificationsCount", ta.unreadNotificationsCountResolver)
	ta.GraphQLClient.RegisterObjectResolver("UnreadNotificationEventsCount", db.NotificationsCountResponse{}, map[string]interface{}{
		"count": func(_ context.Context, q db.NotificationsCountResponse) int64 { return q.Count },
	})

	ta.GraphQLClient.RegisterQueryResolver("credArguments", ta.credArguments)
	ta.GraphQLClient.RegisterObjectResolver("CredArgument", CredArgument{}, map[string]interface{}{
		"creator": func(ctx context.Context, q CredArgument) users.User {
			return getUser(ctx, q.Creator)
		},
		"likes": func(ctx context.Context, q CredArgument) []argument.Like {
			return ta.likesObjectResolver(ctx, app.QueryByIDParams{ID: q.ID})
		},
		// required to retrieve story state, because we only show endorse count once the story is expired
		"story": func(ctx context.Context, q CredArgument) story.Story {
			return ta.storyResolver(ctx, story.QueryStoryByIDParams{ID: q.StoryID})
		},
	})

	ta.GraphQLClient.RegisterQueryResolver("userMetrics", ta.userMetricsResolver)
	ta.GraphQLClient.RegisterObjectResolver("UserMetrics", db.UserMetric{}, map[string]interface{}{
		"asOnDate": func(_ context.Context, q db.UserMetric) string { return q.AsOnDate.Format("2006-01-02") },
		"category": func(ctx context.Context, q db.UserMetric) category.Category {
			return ta.categoryResolver(ctx, category.QueryCategoryByIDParams{ID: q.CategoryID})
		},
	})

	// V2 resolvers
	getEarnedBalance := func(q AppAccount) sdk.Coin {
		amount := sdk.NewCoin(app.StakeDenom, sdk.ZeroInt())
		for _, earned := range q.EarnedStake {
			amount = amount.Add(earned.Coin)
		}
		return amount
	}

	ta.GraphQLClient.RegisterQueryResolver("appAccount", ta.appAccountResolver)
	ta.GraphQLClient.RegisterObjectResolver("AppAccount", AppAccount{}, map[string]interface{}{
		"id":               func(_ context.Context, q AppAccount) string { return q.Address },
		"earnedBalance":    func(_ context.Context, q AppAccount) sdk.Coin { return getEarnedBalance(q) },
		"availableBalance": func(_ context.Context, q AppAccount) sdk.Coin { return q.Coins[0] },
		"twitterProfile": func(ctx context.Context, q AppAccount) db.TwitterProfile {
			return ta.twitterProfileResolver(ctx, q.Address)
		},
	})

	ta.GraphQLClient.RegisterQueryResolver("communities", ta.communitiesResolver)
	ta.GraphQLClient.RegisterQueryResolver("community", ta.communityResolver)
	ta.GraphQLClient.RegisterObjectResolver("Community", Community{}, map[string]interface{}{
		"id": func(_ context.Context, q Community) int64 { return q.ID },
		"iconImage": func(_ context.Context, q Community) string {
			return filepath.Join(ta.APIContext.Config.App.S3AssetsURL, "communities/default_icon_normal.png")
		},
		"heroImage": func(_ context.Context, q Community) string {
			return filepath.Join(ta.APIContext.Config.App.S3AssetsURL, "communities/default_hero.png")
		},
	})

	ta.GraphQLClient.RegisterPaginatedQueryResolverWithFilter("claims", ta.claimsResolver, map[string]interface{}{
		"body": func(_ context.Context, q Claim) string { return q.Body },
	})
	ta.GraphQLClient.RegisterPaginatedObjectResolver("claims", "iD", Claim{}, map[string]interface{}{
		"id": func(_ context.Context, q Claim) int64 { return q.ID },
		"community": func(ctx context.Context, q Claim) Community {
			return ta.communityResolver(ctx, queryByCommunityID{q.CommunityID})
		},
		"source": func(ctx context.Context, q Claim) string { return q.Source.String() },
		"sourceImage": func(ctx context.Context, q Claim) string {
			onImage := og.OgImage{}
			err := og.GetPageDataFromUrl(q.Source.String(), &onImage)
			if err != nil || onImage.Url == "" {
				return filepath.Join(ta.APIContext.Config.App.S3AssetsURL, "sourceImage_default.jpg")
			}
			return onImage.Url
		},
		"argumentCount": func(_ context.Context, q Claim) int64 { return 0 },
		"topArgument":   ta.topArgumentResolver,
		"arguments": func(ctx context.Context, q Claim) []Argument {
			return ta.claimArgumentsResolver(ctx, queryByClaimID{ID: q.ID})
		},
		"stakerCount": func(_ context.Context, q Claim) int64 { return 0 },
		"stakers": func(ctx context.Context, q Claim) []AppAccount {
			return []AppAccount{ta.appAccountResolver(ctx, queryByAddress{ID: q.Creator.String()})}
		},
		"comments": func(ctx context.Context, q Claim) []db.Comment {
			return ta.claimCommentsResolver(ctx, queryByClaimID{ID: q.ID})
		},
		"creator": func(ctx context.Context, q Claim) AppAccount {
			return ta.appAccountResolver(ctx, queryByAddress{ID: q.Creator.String()})
		},
	})
	ta.GraphQLClient.RegisterQueryResolver("claim", ta.claimResolver)

	ta.GraphQLClient.RegisterQueryResolver("claimArguments", ta.claimArgumentsResolver)
	ta.GraphQLClient.RegisterObjectResolver("ClaimArgument", Argument{}, map[string]interface{}{
		"id":          func(_ context.Context, q Argument) int64 { return q.ID },
		"claimId":     func(_ context.Context, q Argument) int64 { return q.ClaimID },
		"vote":        func(_ context.Context, q Argument) bool { return q.Type == Backing },
		"createdTime": func(_ context.Context, q Argument) string { return q.CreatedTime.String() },
		"creator": func(ctx context.Context, q Argument) AppAccount {
			return ta.appAccountResolver(ctx, queryByAddress{ID: q.Creator.String()})
		},
		"hasSlashed":      func(_ context.Context, q Argument) bool { return false },
		"appAccountStake": func(_ context.Context, q Argument) Stake { return Stake{} },
		"stakers": func(ctx context.Context, q Argument) []AppAccount {
			return []AppAccount{ta.appAccountResolver(ctx, queryByAddress{ID: q.Creator.String()})}
		},
	})

	ta.GraphQLClient.RegisterQueryResolver("claimComments", ta.claimCommentsResolver)

	ta.GraphQLClient.RegisterQueryResolver("stakes", ta.stakesResolver)
	ta.GraphQLClient.RegisterObjectResolver("Stake", Stake{}, map[string]interface{}{
		"id": func(_ context.Context, q Stake) int64 { return q.ID },
		"creator": func(ctx context.Context, q Stake) AppAccount {
			return ta.appAccountResolver(ctx, queryByAddress{ID: q.Creator.String()})
		},
	})

	ta.GraphQLClient.BuildSchema()
}
