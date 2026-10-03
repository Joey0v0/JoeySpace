package push

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/segmentio/kafka-go"
	"github.com/yjydist/go-im/internal/ws"
	"go.uber.org/zap"
)

type fakeMessageReader struct {
	msg        kafka.Message
	fetched    bool
	commitErrs []error
	events     *[]string
	cancel     context.CancelFunc
}

func (r *fakeMessageReader) FetchMessage(ctx context.Context) (kafka.Message, error) {
	if !r.fetched {
		r.fetched = true
		*r.events = append(*r.events, "fetch")
		return r.msg, nil
	}
	<-ctx.Done()
	return kafka.Message{}, ctx.Err()
}

func (r *fakeMessageReader) CommitMessages(_ context.Context, messages ...kafka.Message) error {
	*r.events = append(*r.events, "commit")
	if len(messages) != 1 || messages[0].Offset != r.msg.Offset {
		return errors.New("wrong committed message")
	}
	if len(r.commitErrs) > 0 {
		err := r.commitErrs[0]
		r.commitErrs = r.commitErrs[1:]
		return err
	}
	r.cancel()
	return nil
}

func (*fakeMessageReader) Close() error { return nil }

type fakeMessagePusher struct {
	handleErrs []error
	events     *[]string
	cancel     context.CancelFunc
	stopOnFail bool
}

func (p *fakeMessagePusher) HandleMessage(_ context.Context, msg *ws.KafkaChatMsg) error {
	*p.events = append(*p.events, "handle")
	if msg.MsgID != "m1" {
		return errors.New("wrong message")
	}
	if len(p.handleErrs) > 0 {
		err := p.handleErrs[0]
		p.handleErrs = p.handleErrs[1:]
		if p.stopOnFail {
			p.cancel()
		}
		return err
	}
	return nil
}

func TestConsumerCommitsOnlyAfterProcessing(t *testing.T) {
	failure := errors.New("temporary failure")
	for _, tc := range []struct {
		name       string
		value      []byte
		handleErrs []error
		commitErrs []error
		stopOnFail bool
		want       []string
	}{
		{"success", []byte(`{"msg_id":"m1"}`), nil, nil, false, []string{"fetch", "handle", "commit"}},
		{"handler retries before commit", []byte(`{"msg_id":"m1"}`), []error{failure}, nil, false, []string{"fetch", "handle", "handle", "commit"}},
		{"commit retries without redelivery", []byte(`{"msg_id":"m1"}`), nil, []error{failure}, false, []string{"fetch", "handle", "commit", "commit"}},
		{"failed handler is not committed", []byte(`{"msg_id":"m1"}`), []error{failure}, nil, true, []string{"fetch", "handle"}},
		{"malformed message is skipped and committed", []byte(`{`), nil, nil, false, []string{"fetch", "commit"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			var events []string
			reader := &fakeMessageReader{msg: kafka.Message{Offset: 7, Value: tc.value}, events: &events, cancel: cancel, commitErrs: tc.commitErrs}
			pusher := &fakeMessagePusher{events: &events, cancel: cancel, handleErrs: tc.handleErrs, stopOnFail: tc.stopOnFail}
			consumer := &Consumer{reader: reader, pusher: pusher, logger: zap.NewNop()}
			consumer.Start(ctx)
			if !reflect.DeepEqual(events, tc.want) {
				t.Fatalf("calls = %v, want %v", events, tc.want)
			}
		})
	}
}
