package push

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yjydist/go-im/internal/model"
	"go.uber.org/zap"
)

type teamEligibilityResult struct {
	generation int64
	eligible   bool
	err        error
}
type teamEligibilityStub struct {
	results    []teamEligibilityResult
	calls      int
	team, user int64
}

func (s *teamEligibilityStub) CheckCurrentTeamMember(_ context.Context, team, user int64) (int64, bool, error) {
	s.team, s.user = team, user
	result := s.results[min(s.calls, len(s.results)-1)]
	s.calls++
	return result.generation, result.eligible, result.err
}

func teamDeliveryPusher(memberID int64, eligibility TeamEligibility) (*Pusher, *groupDeliveryGroupStub, *groupDeliveryRedisStub, *groupDeliveryMessageStub) {
	team := int64(100)
	groups := &groupDeliveryGroupStub{members: []int64{memberID}, teamID: &team, allowed: true}
	redis := &groupDeliveryRedisStub{}
	messages := &groupDeliveryMessageStub{}
	p := NewPusher(messages, groups, redis, zap.NewNop())
	p.SetTeamEligibility(eligibility)
	return p, groups, redis, messages
}

func TestTeamDeliveryRequiresBothCurrentQualificationsBeforeOffline(t *testing.T) {
	for _, tc := range []struct {
		name        string
		eligible    bool
		userErr     error
		imAllowed   bool
		imErr       error
		wantOffline bool
		wantIMCalls int
	}{
		{"allowed", true, nil, true, nil, true, 1},
		{"left team", false, nil, true, nil, false, 0},
		{"User unavailable", false, errors.New("User down"), true, nil, false, 0},
		{"removed from group", true, nil, false, nil, false, 1},
		{"IM database unavailable", true, nil, true, errors.New("IM down"), false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			user := &teamEligibilityStub{results: []teamEligibilityResult{{generation: 7, eligible: tc.eligible, err: tc.userErr}}}
			p, groups, redis, messages := teamDeliveryPusher(42, user)
			groups.allowed, groups.checkErr = tc.imAllowed, tc.imErr
			err := p.pushToGroup(context.Background(), 300, 1, &model.Message{ID: 9, MsgID: "stable-id"})
			if tc.userErr != nil && !errors.Is(err, tc.userErr) || tc.imErr != nil && !errors.Is(err, tc.imErr) || tc.userErr == nil && tc.imErr == nil && err != nil {
				t.Fatalf("delivery error=%v", err)
			}
			if (len(messages.offlineUsers) > 0) != tc.wantOffline || groups.checkCalls != tc.wantIMCalls || user.calls != 1 || user.team != 100 || user.user != 42 || len(redis.visited) != 1 {
				t.Fatalf("delivery offline=%v IM calls=%d User=%+v Redis=%v", messages.offlineUsers, groups.checkCalls, user, redis.visited)
			}
			if tc.wantIMCalls == 1 && (groups.checkedGroup != 300 || groups.checkedTeam != 100 || groups.checkedUser != 42 || groups.checkedGeneration != 7) {
				t.Fatalf("wrong IM scope: %+v", groups)
			}
		})
	}
}

func TestTeamDeliveryRechecksBeforeOnlineFailureFallback(t *testing.T) {
	for _, tc := range []struct {
		name            string
		second          teamEligibilityResult
		imSecondAllowed bool
		wantError       bool
		wantOffline     bool
	}{
		{"still eligible", teamEligibilityResult{generation: 7, eligible: true}, true, false, true},
		{"left meanwhile", teamEligibilityResult{eligible: false}, true, false, false},
		{"User failed meanwhile", teamEligibilityResult{err: errors.New("User down")}, true, true, false},
		{"group closed meanwhile", teamEligibilityResult{generation: 7, eligible: true}, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
			defer server.Close()
			user := &teamEligibilityStub{results: []teamEligibilityResult{{generation: 7, eligible: true}, tc.second}}
			p, groups, redis, messages := teamDeliveryPusher(42, user)
			redis.onlineAddr = strings.TrimPrefix(server.URL, "http://")
			p.httpClient = server.Client()
			groups.allowed = true
			// The second IM result changes only after the first HTTP attempt.
			if !tc.imSecondAllowed {
				groups.checkErr = nil
				p.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					groups.allowed = false
					return server.Client().Transport.RoundTrip(req)
				})}
			}
			err := p.pushToGroup(context.Background(), 300, 1, &model.Message{ID: 9, MsgID: "stable-id"})
			if (err != nil) != tc.wantError || (len(messages.offlineUsers) == 1) != tc.wantOffline || user.calls != 2 {
				t.Fatalf("err=%v offline=%v User calls=%d", err, messages.offlineUsers, user.calls)
			}
		})
	}
}

func TestTeamDeliveryChecksBeforeOnlineSend(t *testing.T) {
	for _, tc := range []struct {
		name     string
		eligible bool
		wantSend bool
	}{
		{"active member", true, true},
		{"left team", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sent := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { sent++ }))
			defer server.Close()
			user := &teamEligibilityStub{results: []teamEligibilityResult{{generation: 7, eligible: tc.eligible}}}
			p, groups, redis, messages := teamDeliveryPusher(42, user)
			redis.onlineAddr = strings.TrimPrefix(server.URL, "http://")
			p.httpClient = server.Client()
			if err := p.pushToGroup(context.Background(), 300, 1, &model.Message{ID: 9, MsgID: "stable-id"}); err != nil {
				t.Fatal(err)
			}
			if (sent == 1) != tc.wantSend || len(messages.offlineUsers) != 0 || groups.checkCalls != sent {
				t.Fatalf("sent=%d offline=%v IM checks=%d", sent, messages.offlineUsers, groups.checkCalls)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestTeamDeliveryWithoutUserClientNeverFallsBackToLegacy(t *testing.T) {
	p, _, redis, messages := teamDeliveryPusher(42, nil)
	if err := p.pushToGroup(context.Background(), 300, 1, &model.Message{ID: 9, MsgID: "stable-id"}); err == nil || len(redis.visited) != 0 || len(messages.offlineUsers) != 0 {
		t.Fatalf("missing client allowed delivery: %v", err)
	}
}
