package main

import (
	"context"
	"reflect"
	"strings"

	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/internal/rpcauth"
	"github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func triggerMemberResolverReady(resolver triggerMemberResolver) bool {
	if resolver == nil {
		return false
	}
	value := reflect.ValueOf(resolver)
	switch value.Kind() {
	case reflect.Pointer, reflect.Func, reflect.Map, reflect.Slice, reflect.Interface, reflect.Chan:
		return !value.IsNil()
	default:
		return true
	}
}

func triggerMemberMention(source *pb.ReadTaskTriggerContextResponse, name string) bool {
	if strings.Contains(source.GetInstruction(), name) {
		return true
	}
	for _, message := range source.GetMessages() {
		if message != nil && message.GetContentType() == 1 && strings.Contains(message.GetContent(), name) {
			return true
		}
	}
	return false
}

func (s *triggerContextServer) ResolveTaskTriggerMember(ctx context.Context, req *pb.ResolveTaskTriggerMemberRequest) (*pb.ResolveTaskTriggerMemberResponse, error) {
	if ctx == nil {
		return nil, status.Error(codes.Unauthenticated, "verified service TLS identity required")
	}
	dnsName := ""
	if s != nil {
		dnsName = s.agentDNSName
	}
	if err := rpcauth.RequireServiceIdentity(ctx, dnsName); err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	messageID, name := req.GetMessageId(), req.GetName()
	if messageID <= 0 || !model.ValidAgentTriggerMemberName(name) {
		return nil, status.Error(codes.InvalidArgument, "positive trigger message ID and literal name required")
	}
	if s == nil || s.db == nil {
		return nil, status.Error(codes.Unavailable, "trigger member resolution is not enabled")
	}
	resolver, ok := s.teams.(triggerMemberResolver)
	if !ok || !triggerMemberResolverReady(resolver) {
		return nil, status.Error(codes.Unavailable, "trigger member resolution is not enabled")
	}
	ctx, cancel := context.WithTimeout(ctx, triggerContextTimeout)
	defer cancel()
	source, err := s.ReadTaskTriggerContext(ctx, &pb.ReadTaskTriggerContextRequest{MessageId: messageID})
	if err != nil {
		return nil, err
	}
	if !triggerMemberMention(source, name) {
		return nil, triggerContextError(ctx, codes.InvalidArgument, "trigger member name has no authorized text evidence")
	}
	// Freeze the actor's current qualification before resolving candidates. A
	// departure and rejoin during the lookup must not become continuous access.
	scope := model.AgentTriggerOutbox{MessageID: source.GetMessageId(), ActorID: source.GetActorId(), TeamID: source.GetTeamId(), GroupID: source.GetGroupId()}
	baselineGeneration, err := s.currentTriggerGeneration(ctx, scope)
	if err != nil {
		return nil, err
	}
	resolved, err := resolver.Resolve(ctx, source.GetActorId(), source.GetTeamId(), name)
	if ctx.Err() != nil {
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	if err != nil {
		return nil, triggerMemberCallError(ctx, err)
	}
	if !validTriggerMemberResponse(resolved, source.GetActorId(), source.GetTeamId(), name) {
		return nil, triggerContextError(ctx, codes.Unavailable, "invalid trigger member response")
	}
	// Derive this final fence exclusively from the persisted context, not request
	// metadata or the resolver's response scope.
	generation, err := s.currentTriggerGeneration(ctx, scope)
	if err != nil {
		return nil, err
	}
	if generation != baselineGeneration {
		return nil, triggerContextError(ctx, codes.PermissionDenied, "trigger team generation changed while resolving")
	}
	if err := checkTeamGroupReadGeneration(ctx, s.db, scope.GroupID, scope.TeamID, scope.ActorID, generation); err != nil {
		return nil, err
	}
	response := &pb.ResolveTaskTriggerMemberResponse{MessageId: source.GetMessageId(), ActorId: source.GetActorId(), TeamId: source.GetTeamId(),
		GroupId: source.GetGroupId(), RequestKey: source.GetRequestKey(), Name: name, Truncated: resolved.Truncated}
	for _, candidate := range resolved.Candidates {
		response.Candidates = append(response.Candidates, &pb.TaskTriggerMemberCandidate{UserId: candidate.UserId, Username: candidate.Username, Nickname: candidate.Nickname})
	}
	if proto.Size(response) > model.AgentTriggerMemberResponseLimit || ctx.Err() != nil {
		return nil, triggerContextError(ctx, codes.Unavailable, "trigger member response exceeds its limit")
	}
	return response, nil
}
