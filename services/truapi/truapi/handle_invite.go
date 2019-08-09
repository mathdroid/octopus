package truapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/TruStory/octopus/services/truapi/chttp"
	"github.com/TruStory/octopus/services/truapi/db"
	"github.com/TruStory/octopus/services/truapi/truapi/cookies"
	"github.com/TruStory/octopus/services/truapi/truapi/regex"
)

// AddInviteRequest represents the JSON request for adding an invite
type AddInviteRequest struct {
	Email string `json:"email"`
}

// HandleInvite handles requests for invites
func (ta *TruAPI) HandleInvite(r *http.Request) chttp.Response {
	switch r.Method {
	case http.MethodPost:
		return ta.handleCreateInvite(r)
	default:
		return chttp.SimpleErrorResponse(404, Err404ResourceNotFound)
	}
}

func (ta *TruAPI) handleCreateInvite(r *http.Request) chttp.Response {
	request := &AddInviteRequest{}
	err := json.NewDecoder(r.Body).Decode(request)
	if err != nil {
		return chttp.SimpleErrorResponse(400, err)
	}

	email := strings.ToLower(request.Email)
	// check if valid email address
	if !regex.RegexValidEmail.MatchString(email) {
		return chttp.SimpleErrorResponse(422, errors.New("Invalid email address"))
	}

	user, ok := r.Context().Value(userContextKey).(*cookies.AuthenticatedUser)
	if !ok || user == nil {
		return chttp.SimpleErrorResponse(401, Err401NotAuthenticated)
	}

	invite := &db.Invite{
		Creator:     user.Address,
		FriendEmail: email,
	}
	err = ta.DBClient.AddInvite(invite)
	// TODO: error on duplicate entry should return unique error code
	if err != nil {
		return chttp.SimpleErrorResponse(500, err)
	}
	if invite.ID == 0 {
		return chttp.SimpleErrorResponse(422, errors.New("This user has already been invited"))
	}
	respBytes, err := json.Marshal(invite)
	if err != nil {
		return chttp.SimpleErrorResponse(500, err)
	}
	return chttp.SimpleResponse(200, respBytes)
}
