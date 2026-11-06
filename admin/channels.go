package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/lesismal/fit/streaming/channels"
)

// The Admin API's endpoints for the streaming role's channels and push
// targets (I134):
//
//	GET  /streaming/channels          every channel: what it plays and
//	                                  what comes next, its counters and
//	                                  its push targets
//	GET  /streaming/channels/{name}   one channel, by its stream's path
//	POST /streaming/channels/{name}   an action: {"action": "start"},
//	                                  {"action": "stop"}, {"action": "skip"}
//	                                  to the next item, or {"action": "skip",
//	                                  "item": "<id>"} to that item
//	GET  /streaming/push              every push target
//	POST /streaming/push/{name}       an action: start, stop or restart
//
// An action answers with the channel's or target's status. It is not kept
// in the config: a channel stopped stays stopped across reloads that keep
// it, and runs again when fit or the role's hub starts again.

// channelManager is the channels of the streaming role, or nil once it has
// answered 404.
func (s *Server) channelManager(w http.ResponseWriter) *channels.Manager {
	m := channels.Lookup(s.configs.env.Engine)
	if m == nil {
		writeError(w, http.StatusNotFound, "the streaming role does not run here")
	}
	return m
}

// listChannels answers GET /streaming/channels.
func (s *Server) listChannels(w http.ResponseWriter, _ *http.Request) {
	m := s.channelManager(w)
	if m == nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"channels": m.Channels()})
}

// channel is the channel the request names, or nil once it has answered
// 404.
func (s *Server) channel(w http.ResponseWriter, r *http.Request) *channels.Channel {
	m := s.channelManager(w)
	if m == nil {
		return nil
	}
	c := m.Channel(r.PathValue("name"))
	if c == nil {
		writeError(w, http.StatusNotFound, "no channel named "+strconv.Quote(r.PathValue("name")))
	}
	return c
}

// getChannel answers GET /streaming/channels/{name...}.
func (s *Server) getChannel(w http.ResponseWriter, r *http.Request) {
	if c := s.channel(w, r); c != nil {
		writeJSON(w, http.StatusOK, c.Status())
	}
}

// action is the body of an action's POST.
type action struct {
	Action string `json:"action"`
	Item   string `json:"item,omitempty"`
}

// readAction decodes an action's body.
func (s *Server) readAction(w http.ResponseWriter, r *http.Request) (action, bool) {
	var a action
	data, ok := s.readBody(w, r, "application/json")
	if !ok {
		return a, false
	}
	if err := json.Unmarshal(data, &a); err != nil {
		writeError(w, http.StatusBadRequest, "the body: "+err.Error())
		return a, false
	}
	return a, true
}

// channelAction answers POST /streaming/channels/{name...}.
func (s *Server) channelAction(w http.ResponseWriter, r *http.Request) {
	c := s.channel(w, r)
	if c == nil {
		return
	}
	a, ok := s.readAction(w, r)
	if !ok {
		return
	}
	var err error
	switch a.Action {
	case "start":
		c.Start()
	case "stop":
		c.Stop()
	case "skip":
		err = c.Skip(a.Item)
	default:
		err = channels.ErrAction
	}
	switch {
	case errors.Is(err, channels.ErrStopped):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, channels.ErrNoItem):
		writeError(w, http.StatusNotFound, err.Error())
	case err != nil:
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeJSON(w, http.StatusOK, c.Status())
	}
}

// listPush answers GET /streaming/push.
func (s *Server) listPush(w http.ResponseWriter, _ *http.Request) {
	m := s.channelManager(w)
	if m == nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"push": m.Pushes()})
}

// pushAction answers POST /streaming/push/{name...}.
func (s *Server) pushAction(w http.ResponseWriter, r *http.Request) {
	m := s.channelManager(w)
	if m == nil {
		return
	}
	p := m.Push(r.PathValue("name"))
	if p == nil {
		writeError(w, http.StatusNotFound, "no push target named "+strconv.Quote(r.PathValue("name")))
		return
	}
	a, ok := s.readAction(w, r)
	if !ok {
		return
	}
	switch a.Action {
	case "start":
		p.Start()
	case "stop":
		p.Stop()
	case "restart":
		p.Restart()
	default:
		writeError(w, http.StatusBadRequest, channels.ErrAction.Error())
		return
	}
	writeJSON(w, http.StatusOK, p.Status())
}
