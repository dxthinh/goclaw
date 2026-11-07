package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/i18n"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

type thresholdApprovalStore struct {
	store.EvolutionSuggestionStore
	suggestion store.EvolutionSuggestion
	writes     int
}

func (s *thresholdApprovalStore) GetSuggestion(context.Context, uuid.UUID) (*store.EvolutionSuggestion, error) {
	return &s.suggestion, nil
}

func (s *thresholdApprovalStore) UpdateSuggestionStatus(context.Context, uuid.UUID, string, string) error {
	s.writes++
	return nil
}

func (s *thresholdApprovalStore) UpdateSuggestionParameters(context.Context, uuid.UUID, json.RawMessage) error {
	s.writes++
	return nil
}

func TestThresholdApprovalRejectsWithoutWriting(t *testing.T) {
	for _, locale := range []string{"en", "vi", "zh"} {
		t.Run(locale, func(t *testing.T) {
			suggestions := &thresholdApprovalStore{suggestion: store.EvolutionSuggestion{
				ID: uuid.New(), AgentID: uuid.New(), SuggestionType: store.SuggestThreshold, Status: "pending",
			}}
			handler := NewEvolutionHandler(nil, suggestions)
			mux := http.NewServeMux()
			mux.HandleFunc("PATCH /agents/{agentID}/suggestions/{suggestionID}", handler.handleUpdateSuggestion)
			request := httptest.NewRequest(http.MethodPatch,
				"/agents/"+suggestions.suggestion.AgentID.String()+"/suggestions/"+suggestions.suggestion.ID.String(),
				strings.NewReader(`{"status":"approved"}`))
			request = request.WithContext(store.WithLocale(request.Context(), locale))
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			var body map[string]string
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusBadRequest || body["error"] != i18n.T(locale, i18n.MsgRetrievalThresholdUnsupported) {
				t.Fatalf("approval response: status=%d body=%v", response.Code, body)
			}
			if suggestions.writes != 0 || suggestions.suggestion.Status != "pending" {
				t.Fatalf("unsupported approval changed state: writes=%d status=%s", suggestions.writes, suggestions.suggestion.Status)
			}
		})
	}
}
