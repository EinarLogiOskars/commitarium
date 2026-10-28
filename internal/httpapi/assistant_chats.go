package httpapi

import (
	"context"
	"net/http"

	"github.com/EinarLogiOskars/commitarium/internal/conversations"
)

// Chats (ADR-038): a scope holds many chats sharing one assistant. The
// routes under .../chats/{chat} act on that chat; the scope's own routes act
// on its most recently used chat, as they did on its one conversation.

// chatManager lists, starts, renames, and archives a scope's chats.
type chatManager interface {
	Chats(ctx context.Context, scope string, archived bool) ([]conversations.Chat, error)
	CreateChat(ctx context.Context, scope, title string) (conversations.Conversation, error)
	RenameChat(ctx context.Context, scope, chat, title string) (conversations.Chat, error)
	ArchiveChat(ctx context.Context, scope, chat string) (conversations.Chat, error)
}

// registerChatRoutes adds the chat routes; the chat's own message, retry,
// focus, action, and autopilot routes are the scope's, in the chat.
func (api *API) registerChatRoutes(mux *http.ServeMux) {
	const chat = "/api/v1/assistant/conversations/{scope}/chats/{chat}"
	mux.HandleFunc("GET /api/v1/assistant/conversations/{scope}/chats", api.listChatsHandler)
	mux.HandleFunc("POST /api/v1/assistant/conversations/{scope}/chats", api.createChatHandler)
	mux.HandleFunc("GET "+chat, inChat(api.getConversationHandler))
	mux.HandleFunc("PATCH "+chat, api.renameChatHandler)
	mux.HandleFunc("DELETE "+chat, api.archiveChatHandler)
	mux.HandleFunc("POST "+chat+"/messages", inChat(api.sendConversationHandler))
	mux.HandleFunc("POST "+chat+"/retry", inChat(api.retryConversationTurnHandler))
	mux.HandleFunc("PUT "+chat+"/focus", inChat(api.setConversationFocusHandler))
	mux.HandleFunc("DELETE "+chat+"/focus", inChat(api.setConversationFocusHandler))
	mux.HandleFunc("POST "+chat+"/actions/{actionID}/approve", inChat(api.approveConversationActionHandler))
	mux.HandleFunc("POST "+chat+"/actions/{actionID}/dismiss", inChat(api.dismissConversationActionHandler))
	mux.HandleFunc("POST "+chat+"/autopilot", inChat(api.startAutopilotHandler))
	mux.HandleFunc("POST /api/v1/assistant/conversations/"+conversations.TopLevel+"/chats/{chat}/project", api.makeProjectHandler)
}

// inChat has a scope's handler act on the route's chat.
func inChat(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		handler(w, r.WithContext(conversations.InChat(r.Context(), r.PathValue("chat"))))
	}
}

func (api *API) chats(w http.ResponseWriter) (chatManager, bool) {
	manager, ok := api.conversations.(chatManager)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "assistant_unavailable", "the assistant's chats are unavailable")
	}
	return manager, ok
}

// listChatsHandler answers a scope's chats, the most recently used first;
// ?archived=true includes the archived ones.
func (api *API) listChatsHandler(w http.ResponseWriter, r *http.Request) {
	manager, ok := api.chats(w)
	if !ok {
		return
	}
	listed, err := manager.Chats(r.Context(), r.PathValue("scope"), r.URL.Query().Get("archived") == "true")
	if err != nil {
		writeConversationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"chats": listed}, "assistant chats")
}

type chatRequest struct {
	Title string `json:"title"`
}

func (api *API) createChatHandler(w http.ResponseWriter, r *http.Request) {
	manager, ok := api.chats(w)
	if !ok {
		return
	}
	var body chatRequest
	if r.ContentLength != 0 && !decodeSingleJSON(w, r, &body) {
		return
	}
	conversation, err := manager.CreateChat(r.Context(), r.PathValue("scope"), body.Title)
	if err != nil {
		writeConversationError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, conversation, "assistant chat")
}

func (api *API) renameChatHandler(w http.ResponseWriter, r *http.Request) {
	manager, ok := api.chats(w)
	if !ok {
		return
	}
	var body chatRequest
	if !decodeSingleJSON(w, r, &body) {
		return
	}
	renamed, err := manager.RenameChat(r.Context(), r.PathValue("scope"), r.PathValue("chat"), body.Title)
	if err != nil {
		writeConversationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, renamed, "assistant chat")
}

// archiveChatHandler archives a chat: it leaves the list, keeps its
// history, and no longer receives events.
func (api *API) archiveChatHandler(w http.ResponseWriter, r *http.Request) {
	manager, ok := api.chats(w)
	if !ok {
		return
	}
	archived, err := manager.ArchiveChat(r.Context(), r.PathValue("scope"), r.PathValue("chat"))
	if err != nil {
		writeConversationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, archived, "assistant chat")
}
