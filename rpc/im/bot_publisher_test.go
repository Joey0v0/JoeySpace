package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/segmentio/kafka-go"
	"github.com/yjydist/go-im/internal/model"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type memoryBotSends struct {
	record     botSendRecord
	prepareErr error
	markErr    error
}

func (s *memoryBotSends) Prepare(_ context.Context, i botSendIntent) (botSendRecord, error) {
	if s.prepareErr != nil {
		return botSendRecord{}, s.prepareErr
	}
	if s.record.MsgID == "" {
		s.record = storedBotRecord(i)
	}
	if !s.record.sameIntent(i) {
		return botSendRecord{}, status.Error(codes.AlreadyExists, "conflict")
	}
	return s.record, nil
}
func (s *memoryBotSends) MarkAccepted(_ context.Context, _ string) error {
	if s.markErr != nil {
		return s.markErr
	}
	s.record.Accepted = true
	return nil
}

type botWriterStub struct {
	messages []kafka.Message
	err      error
}

func (w *botWriterStub) WriteMessages(_ context.Context, m ...kafka.Message) error {
	w.messages = append(w.messages, m...)
	return w.err
}

func TestBotPublisherRetriesExactMessageAfterUncertainResult(t *testing.T) {
	for _, failure := range []string{"Kafka response lost", "acceptance write failed"} {
		t.Run(failure, func(t *testing.T) {
			store, writer := &memoryBotSends{}, &botWriterStub{}
			if failure == "Kafka response lost" {
				writer.err = errors.New("response lost")
			} else {
				store.markErr = status.Error(codes.Unavailable, "write lost")
			}
			i := validBotIntent(t)
			p := &kafkaBotPublisher{store: store, writer: writer}
			if id, err := p.Publish(context.Background(), i); id != "" || status.Code(err) != codes.Unavailable || store.record.Accepted {
				t.Fatalf("must not claim acceptance: %s %v", id, err)
			}
			writer.err, store.markErr = nil, nil
			// Rebuild publisher while retaining durable-state substitute.
			p = &kafkaBotPublisher{store: store, writer: writer}
			id, err := p.Publish(context.Background(), i)
			if err != nil || id != "bot-task:9" || !store.record.Accepted || len(writer.messages) != 2 || !bytes.Equal(writer.messages[0].Value, writer.messages[1].Value) {
				t.Fatalf("retry changed message: %s %v", id, err)
			}
			var event model.ChatEvent
			if err := json.Unmarshal(writer.messages[1].Value, &event); err != nil || event.FromID != 7 || event.SenderType != 2 || event.InitiatorID != 42 || event.ToID != 300 || event.ChatType != 2 || event.ContentType != 4 || event.Content != i.Content || event.Timestamp != store.record.TimestampUnixMs || string(writer.messages[1].Key) != "300" {
				t.Fatalf("invalid Kafka payload: %+v %v", event, err)
			}
			if _, err := p.Publish(context.Background(), i); err != nil || len(writer.messages) != 2 {
				t.Fatal("accepted result republished")
			}
			i.GroupID++
			if _, err := p.Publish(context.Background(), i); status.Code(err) != codes.AlreadyExists || len(writer.messages) != 2 {
				t.Fatal("conflicting scope published")
			}
		})
	}
}

func TestBotPublisherDoesNotWriteWhenPersistenceFails(t *testing.T) {
	w := &botWriterStub{}
	p := &kafkaBotPublisher{store: &memoryBotSends{prepareErr: status.Error(codes.Unavailable, "DB failed")}, writer: w}
	if _, err := p.Publish(context.Background(), validBotIntent(t)); status.Code(err) != codes.Unavailable || len(w.messages) != 0 {
		t.Fatal(err)
	}
}
