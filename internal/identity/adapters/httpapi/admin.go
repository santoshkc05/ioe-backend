package httpapi

import (
	"net/http"

	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/platform/problem"
)

type usersResponse struct {
	Users []userResponse `json:"users"`
}

type setRoleRequest struct {
	Role string `json:"role"`
}

func (h *Handler) searchUsers(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context()) // RequireAuth guarantees presence
	users, err := h.admin.SearchUsers(r.Context(), p, r.URL.Query().Get("email"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := usersResponse{Users: make([]userResponse, 0, len(users))}
	for _, u := range users {
		out.Users = append(out.Users, toUserResponse(u))
	}
	httpserver.WriteJSON(w, http.StatusOK, out)
}

func (h *Handler) setRole(w http.ResponseWriter, r *http.Request) {
	userID, err := id.Parse(r.PathValue("userID"))
	if err != nil {
		problem.Write(w, r, http.StatusNotFound, problem.TypeNotFound, "Not Found", "")
		return
	}
	var req setRoleRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	p, _ := auth.PrincipalFrom(r.Context()) // RequireAuth guarantees presence
	u, err := h.admin.SetRole(r.Context(), p, userID, auth.Role(req.Role))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toUserResponse(u))
}
