package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Set both variables only for a disposable MySQL 8 database initialized with deploy/mysql/init.sql.
func TestDirectUnreadRealMySQLAndRPC(t *testing.T) {
	if os.Getenv("IM_MYSQL_INTEGRATION_ISOLATED") != "1" || os.Getenv("IM_MYSQL_INTEGRATION_DSN") == "" {
		t.Skip("requires an explicitly isolated MySQL integration database")
	}
	db, err := gorm.Open(mysql.Open(os.Getenv("IM_MYSQL_INTEGRATION_DSN")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("cannot open isolated MySQL integration database")
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })

	base := time.Now().UnixNano()
	for _, row := range []struct{ id, from, to int64 }{
		{base + 1, 43, 42}, {base + 2, 42, 43}, {base + 3, 43, 42},
		{base + 4, 44, 42}, {base + 5, 43, 42},
	} {
		if err := tx.Exec("INSERT INTO messages (id,msg_id,from_id,to_id,chat_type,content_type,content) VALUES (?,?,?,?,1,1,?)",
			row.id, fmt.Sprintf("im-direct-integration-%d", row.id), row.from, row.to, "test message").Error; err != nil {
			t.Fatal(err)
		}
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	pb.RegisterIMServer(server, &imServer{db: tx, jwtSecret: imTestSecret})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+validIMToken(t)))
	client := pb.NewIMClient(conn)

	page, err := client.ListDirectMessages(ctx, &pb.ListDirectMessagesRequest{PeerId: 43, Limit: 2})
	if err != nil || len(page.GetMessages()) != 2 || page.GetMessages()[0].GetId() != base+5 ||
		page.GetMessages()[1].GetId() != base+3 || page.GetNextBeforeMessageId() != base+3 {
		t.Fatalf("first history page=%v err=%v", page, err)
	}
	older, err := client.ListDirectMessages(ctx, &pb.ListDirectMessagesRequest{PeerId: 43, BeforeMessageId: base + 3, Limit: 2})
	if err != nil || len(older.GetMessages()) != 2 || older.GetMessages()[0].GetId() != base+2 ||
		older.GetMessages()[1].GetId() != base+1 || older.GetNextBeforeMessageId() != 0 {
		t.Fatalf("older history page=%v err=%v", older, err)
	}
	initial, err := client.GetDirectUnread(ctx, &pb.GetDirectUnreadRequest{PeerId: 43})
	if err != nil || initial.GetUnreadCount() != 3 {
		t.Fatalf("initial unread=%v err=%v", initial, err)
	}
	ids := []int64{base + 3, base + 1, base + 3}
	var firstReadTimes []time.Time
	for attempt := 0; attempt < 2; attempt++ {
		marked, err := client.MarkDirectMessagesRead(ctx, &pb.MarkDirectMessagesReadRequest{PeerId: 43, MessageIds: ids})
		if err != nil || marked.GetUnreadCount() != 1 || len(marked.GetMessageIds()) != 2 ||
			marked.GetMessageIds()[0] != base+1 || marked.GetMessageIds()[1] != base+3 {
			t.Fatalf("mark attempt %d=%v err=%v", attempt, marked, err)
		}
		var readRows []struct {
			MessageID int64
			ReadAt    time.Time
		}
		if err := tx.Table("im_direct_message_reads").Select("message_id, read_at").
			Where("user_id = ? AND peer_id = ?", 42, 43).Order("message_id").Find(&readRows).Error; err != nil ||
			len(readRows) != 2 || readRows[0].MessageID != base+1 || readRows[1].MessageID != base+3 ||
			readRows[0].ReadAt.IsZero() || readRows[1].ReadAt.IsZero() {
			t.Fatalf("persisted reads after attempt %d=%v err=%v", attempt, readRows, err)
		}
		if attempt == 0 {
			firstReadTimes = []time.Time{readRows[0].ReadAt, readRows[1].ReadAt}
		} else if !readRows[0].ReadAt.Equal(firstReadTimes[0]) || !readRows[1].ReadAt.Equal(firstReadTimes[1]) {
			t.Fatalf("replayed read changed the original read_at: %v", readRows)
		}
	}
	if _, err := client.MarkDirectMessagesRead(ctx, &pb.MarkDirectMessagesReadRequest{PeerId: 43, MessageIds: []int64{base + 5, base + 2}}); status.Code(err) != codes.NotFound {
		t.Fatalf("mixed incoming/outgoing batch must fail: %v", err)
	}
	remaining, err := client.GetDirectUnread(ctx, &pb.GetDirectUnreadRequest{PeerId: 43})
	if err != nil || remaining.GetUnreadCount() != 1 {
		t.Fatalf("failed batch changed unread=%v err=%v", remaining, err)
	}
}
