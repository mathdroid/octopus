package truapi

import (
	"context"
	"net/http"
	"net/url"
	"time"

	app "github.com/TruStory/truchain/types"
	"github.com/TruStory/truchain/x/backing"
	"github.com/TruStory/truchain/x/category"
	"github.com/TruStory/truchain/x/challenge"
	"github.com/TruStory/truchain/x/chttp"
	"github.com/TruStory/truchain/x/db"
	"github.com/TruStory/truchain/x/game"
	"github.com/TruStory/truchain/x/graphql"
	"github.com/TruStory/truchain/x/params"
	"github.com/TruStory/truchain/x/story"
	"github.com/TruStory/truchain/x/users"
	"github.com/TruStory/truchain/x/vote"
	sdk "github.com/cosmos/cosmos-sdk/types"
	thunder "github.com/samsarahq/thunder/graphql"
	"github.com/samsarahq/thunder/graphql/graphiql"
)

// TruAPI implements an HTTP server for TruStory functionality using `chttp.API`
type TruAPI struct {
	*chttp.API
	GraphQLClient *graphql.Client
	DBClient      db.Datastore
}

// NewTruAPI returns a `TruAPI` instance populated with the existing app and a new GraphQL client
func NewTruAPI(aa *chttp.App) *TruAPI {
	ta := TruAPI{
		API:           chttp.NewAPI(aa, supported),
		GraphQLClient: graphql.NewGraphQLClient(),
		DBClient:      db.NewDBClient(),
	}

	return &ta
}

// RegisterModels registers types for off-chain DB models
func (ta *TruAPI) RegisterModels() {
	err := ta.DBClient.RegisterModel(&db.TwitterProfile{})
	if err != nil {
		panic(err)
	}
}

// RegisterRoutes applies the TruStory API routes to the `chttp.API` router
func (ta *TruAPI) RegisterRoutes() {
	// Register routes for Trustory React web app
	fs := http.FileServer(http.Dir("web/static"))
	http.Handle("/static/", http.StripPrefix("/static/", fs))
	http.HandleFunc("/web/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "web/index.html")
	})

	ta.Use(chttp.JSONResponseMiddleware)
	http.Handle("/graphql", thunder.Handler(ta.GraphQLClient.Schema))
	http.Handle("/graphiql/", http.StripPrefix("/graphiql/", graphiql.Handler()))
	ta.HandleFunc("/ping", ta.HandlePing)
	ta.HandleFunc("/graphql", ta.HandleGraphQL)
	ta.HandleFunc("/presigned", ta.HandlePresigned)
	ta.HandleFunc("/register", ta.HandleRegistration)
}

// RegisterResolvers builds the app's GraphQL schema from resolvers (declared in `resolver.go`)
func (ta *TruAPI) RegisterResolvers() {
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

	getChallenges := func(ctx context.Context, gameID int64) []challenge.Challenge {
		return ta.challengesResolver(ctx, app.QueryByIDParams{ID: gameID})
	}

	getVotes := func(ctx context.Context, gameID int64) []vote.TokenVote {
		return ta.votesResolver(ctx, app.QueryByIDParams{ID: gameID})
	}

	ta.GraphQLClient.RegisterQueryResolver("backing", ta.backingResolver)
	ta.GraphQLClient.RegisterObjectResolver("Backing", backing.Backing{}, map[string]interface{}{
		"amount":    func(ctx context.Context, q backing.Backing) sdk.Coin { return q.Amount() },
		"argument":  func(ctx context.Context, q backing.Backing) string { return q.Argument },
		"interest":  func(ctx context.Context, q backing.Backing) sdk.Coin { return q.Interest },
		"vote":      func(ctx context.Context, q backing.Backing) bool { return q.VoteChoice() },
		"creator":   func(ctx context.Context, q backing.Backing) users.User { return getUser(ctx, q.Creator()) },
		"timestamp": func(ctx context.Context, q backing.Backing) app.Timestamp { return q.Timestamp },

		// Deprecated: now part of argument
		"evidence": func(ctx context.Context, q backing.Backing) []url.URL { return []url.URL{} },
		// Deprecated: renamed to maturesTime
		"expires": func(ctx context.Context, q backing.Backing) time.Time { return q.MaturesTime },
	})

	ta.GraphQLClient.RegisterQueryResolver("categories", ta.allCategoriesResolver)
	ta.GraphQLClient.RegisterQueryResolver("category", ta.categoryResolver)
	ta.GraphQLClient.RegisterObjectResolver("Category", category.Category{}, map[string]interface{}{
		"id":      func(_ context.Context, q category.Category) int64 { return q.ID },
		"stories": ta.categoryStoriesResolver,

		// Deprecated: categories no longer have a creator
		"creator": func(ctx context.Context, q category.Category) users.User { return getUser(ctx, sdk.AccAddress{}) },
	})

	ta.GraphQLClient.RegisterQueryResolver("challenge", ta.challengeResolver)
	ta.GraphQLClient.RegisterObjectResolver("Challenge", challenge.Challenge{}, map[string]interface{}{
		"amount":    func(ctx context.Context, q challenge.Challenge) sdk.Coin { return q.Amount() },
		"argument":  func(ctx context.Context, q challenge.Challenge) string { return q.Argument },
		"vote":      func(ctx context.Context, q challenge.Challenge) bool { return q.VoteChoice() },
		"creator":   func(ctx context.Context, q challenge.Challenge) users.User { return getUser(ctx, q.Creator()) },
		"timestamp": func(ctx context.Context, q challenge.Challenge) app.Timestamp { return q.Timestamp },

		// Deprecated: now part of argument
		"evidence": func(ctx context.Context, q challenge.Challenge) []url.URL { return []url.URL{} },
	})

	ta.GraphQLClient.RegisterObjectResolver("Coin", sdk.Coin{}, map[string]interface{}{
		"amount": func(_ context.Context, q sdk.Coin) string { return q.Amount.String() },
		"denom":  func(_ context.Context, q sdk.Coin) string { return q.Denom },
		"unit":   func(_ context.Context, q sdk.Coin) string { return "preethi" },
	})

	ta.GraphQLClient.RegisterQueryResolver("params", ta.paramsResolver)
	ta.GraphQLClient.RegisterObjectResolver("Params", params.Params{}, map[string]interface{}{
		"backingAmountWeight":    func(_ context.Context, p params.Params) string { return p.BackingAmountWeight.String() },
		"backingPeriodWeight":    func(_ context.Context, p params.Params) string { return p.BackingPeriodWeight.String() },
		"backingMinInterestRate": func(_ context.Context, p params.Params) string { return p.BackingMinInterestRate.String() },
		"backingMaxInterestRate": func(_ context.Context, p params.Params) string { return p.BackingMaxInterestRate.String() },
	})

	ta.GraphQLClient.RegisterObjectResolver("Game", game.Game{}, map[string]interface{}{
		"id":              func(_ context.Context, q game.Game) int64 { return q.ID },
		"creator":         func(ctx context.Context, q game.Game) users.User { return getUser(ctx, q.Creator) },
		"challengePool":   func(_ context.Context, q game.Game) sdk.Coin { return q.ChallengePool },
		"totalVoteAmount": ta.votesTotalAmountResolver,

		// Deprecated: remove in favor of challengeThreshold on a Story because a challenge threshold exists even when a game does not
		"challengeThreshold": func(_ context.Context, q game.Game) sdk.Coin { return sdk.Coin{} },
		// Deprecated: remove in favor of the auto-resolving field `challengeExpireTime`
		"expiresTime": func(_ context.Context, q game.Game) time.Time { return q.ChallengeExpireTime },
		// Deprecated: remove in favor of the auto-resolving field `votingEndTime`
		"votingPeriodEndTime": func(_ context.Context, q game.Game) time.Time { return q.VotingEndTime },
	})

	ta.GraphQLClient.RegisterQueryResolver("stories", ta.allStoriesResolver)
	ta.GraphQLClient.RegisterQueryResolver("story", ta.storyResolver)
	ta.GraphQLClient.RegisterObjectResolver("Story", story.Story{}, map[string]interface{}{
		"id":                 func(_ context.Context, q story.Story) int64 { return q.ID },
		"backings":           func(ctx context.Context, q story.Story) []backing.Backing { return getBackings(ctx, q.ID) },
		"challenges":         func(ctx context.Context, q story.Story) []challenge.Challenge { return getChallenges(ctx, q.ID) },
		"backingTotal":       ta.backingTotalResolver,
		"challengeThreshold": ta.challengeThresholdResolver,
		"category":           ta.storyCategoryResolver,
		"creator":            func(ctx context.Context, q story.Story) users.User { return getUser(ctx, q.Creator) },
		"source":             func(ctx context.Context, q story.Story) string { return q.Source.String() },
		"game":               ta.gameResolver,
		"votes":              func(ctx context.Context, q story.Story) []vote.TokenVote { return getVotes(ctx, q.ID) },

		// Deprecated: now part of argument
		"evidence": func(ctx context.Context, q story.Story) []url.URL { return []url.URL{} },
		// Deprecated: arguments are now only on BCV
		"argument": func(ctx context.Context, q story.Story) string { return "" },
	})

	ta.GraphQLClient.RegisterObjectResolver("TwitterProfile", db.TwitterProfile{}, map[string]interface{}{
		"id": func(_ context.Context, q db.TwitterProfile) string { return string(q.ID) },
	})

	ta.GraphQLClient.RegisterQueryResolver("users", ta.usersResolver)
	ta.GraphQLClient.RegisterObjectResolver("User", users.User{}, map[string]interface{}{
		"id":             func(_ context.Context, q users.User) string { return q.Address },
		"coins":          func(_ context.Context, q users.User) sdk.Coins { return q.Coins },
		"pubkey":         func(_ context.Context, q users.User) string { return q.Pubkey.String() },
		"twitterProfile": ta.twitterProfileResolver,
	})

	ta.GraphQLClient.RegisterObjectResolver("URL", url.URL{}, map[string]interface{}{
		"url": func(_ context.Context, q url.URL) string { return q.String() },
	})

	ta.GraphQLClient.RegisterQueryResolver("vote", ta.voteResolver)
	ta.GraphQLClient.RegisterObjectResolver("Vote", vote.TokenVote{}, map[string]interface{}{
		"amount":    func(ctx context.Context, q vote.TokenVote) sdk.Coin { return q.Amount() },
		"argument":  func(ctx context.Context, q vote.TokenVote) string { return q.Argument },
		"vote":      func(ctx context.Context, q vote.TokenVote) bool { return q.VoteChoice() },
		"creator":   func(ctx context.Context, q vote.TokenVote) users.User { return getUser(ctx, q.Creator()) },
		"timestamp": func(ctx context.Context, q vote.TokenVote) app.Timestamp { return q.Timestamp },

		// Deprecated: now part of argument
		"evidence": func(ctx context.Context, q vote.TokenVote) []url.URL { return []url.URL{} },
	})

	ta.GraphQLClient.BuildSchema()
}
