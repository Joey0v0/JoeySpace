package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yjydist/go-im/rpc/agent/pb"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type collectionPreparerFunc func(context.Context, *pb.PrepareTaskDraftRequest) (*pb.PrepareTaskDraftResponse, error)

func (f collectionPreparerFunc) PrepareTaskDraftCollection(ctx context.Context, req *pb.PrepareTaskDraftRequest, _ ...grpc.CallOption) (*pb.PrepareTaskDraftResponse, error) {
	return f(ctx, req)
}

type collectionReaderFunc func(context.Context, *pb.GetTaskDraftRequest) (*pb.GetTaskDraftCollectionResponse, error)

func (f collectionReaderFunc) GetTaskDraftCollection(ctx context.Context, req *pb.GetTaskDraftRequest, _ ...grpc.CallOption) (*pb.GetTaskDraftCollectionResponse, error) {
	return f(ctx, req)
}

type collectionItemReaderFunc func(context.Context, *pb.GetTaskDraftItemRequest) (*pb.GetTaskDraftItemResponse, error)

func (f collectionItemReaderFunc) GetTaskDraftItem(ctx context.Context, req *pb.GetTaskDraftItemRequest, _ ...grpc.CallOption) (*pb.GetTaskDraftItemResponse, error) {
	return f(ctx, req)
}

func collectionHTTPRequest(runID, index string, withIndex bool) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/agent/runs/"+runID+"/drafts", nil)
	vars := map[string]string{"run_id": runID}
	if withIndex {
		vars["item_index"] = index
	}
	r = pathvar.WithVars(r, vars)
	r.Header.Set("Authorization", "Bearer user-token")
	return r
}

func validHTTPCollection() *pb.GetTaskDraftCollectionResponse {
	items := make([]*pb.TaskDraftCollectionItem, 2)
	for i := range items {
		index := int32(i)
		items[i] = &pb.TaskDraftCollectionItem{ItemIndex: &index, Status: "waiting_confirmation", ReplyStatus: "disabled", Draft: &pb.TaskDraftItem{
			Revision: 9007199254740993, Title: "Task", Description: "Notes", AssigneeId: 9007199254740995, AssigneeName: "李四", AssigneeResolution: "matched", SourceMessageId: 9007199254740997,
			Deadline: &pb.TaskDraftDeadline{Source: "none", Timezone: "Asia/Shanghai", Resolution: "none", InstructionReferenceUnixMs: 1791097200123},
		}}
	}
	return &pb.GetTaskDraftCollectionResponse{RunId: 9007199254740999, TeamId: 9007199254740993, GroupId: 9007199254740995, ItemCount: 2, Items: items}
}

func TestPrepareCollectionHTTPReusesFieldsTokenKeyAndExactResult(t *testing.T) {
	for _, withReference := range []bool{false, true} {
		client := collectionPreparerFunc(func(ctx context.Context, req *pb.PrepareTaskDraftRequest) (*pb.PrepareTaskDraftResponse, error) {
			md, _ := metadata.FromOutgoingContext(ctx)
			if req.GetTeamId() != 9007199254740993 || req.GetGroupId() != 9007199254740995 || req.GetInstruction() != "提取待办" ||
				len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer user-token" || len(md.Get("idempotency-key")) != 1 || md.Get("idempotency-key")[0] != "collection-key" ||
				(req.InstructionReferenceUnixMs != nil) != withReference || (withReference && req.GetInstructionReferenceUnixMs() != 1791097200123) {
				t.Fatalf("request %v md %v", req, md)
			}
			return &pb.PrepareTaskDraftResponse{RunId: 9007199254740999}, nil
		})
		body := `{"instruction":" 提取待办 "}`
		if withReference {
			body = `{"instruction":" 提取待办 ","instruction_reference_unix_ms":1791097200123}`
		}
		w := httptest.NewRecorder()
		prepareTaskDraftCollectionHandler(client)(w, prepareDraftHTTPRequest("9007199254740993", "9007199254740995", body, "collection-key"))
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"run_id":"9007199254740999"`) {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	}
}

func TestPrepareCollectionHTTPRejectsInvalidRequestsBeforeRPC(t *testing.T) {
	client := collectionPreparerFunc(func(context.Context, *pb.PrepareTaskDraftRequest) (*pb.PrepareTaskDraftResponse, error) {
		t.Fatal("invalid request reached RPC")
		return nil, nil
	})
	for _, body := range []string{`{"instruction":" "}`, `{"instruction":"x","user_id":1}`, `{"instruction":"x","instruction_reference_unix_ms":null}`, `{"instruction":"x","instruction_reference_unix_ms":0}`, `{"instruction":"x","instruction_reference_unix_ms":"1"}`, `{"instruction":"x","instruction_reference_unix_ms":1.5}`, `{"instruction":"x","instruction_reference_unix_ms":253402300800000}`, `{"instruction":"x"}{}`, `{"instruction":"` + strings.Repeat("x", 2001) + `"}`} {
		w := httptest.NewRecorder()
		prepareTaskDraftCollectionHandler(client)(w, prepareDraftHTTPRequest("2", "3", body, "collection-key"))
		if w.Code != 400 {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body.String())
		}
	}
	for _, key := range []string{"", "bad key"} {
		w := httptest.NewRecorder()
		prepareTaskDraftCollectionHandler(client)(w, prepareDraftHTTPRequest("2", "3", `{"instruction":"x"}`, key))
		if w.Code != 400 {
			t.Fatalf("key %q: %d", key, w.Code)
		}
	}
	r := prepareDraftHTTPRequest("2", "3", `{"instruction":"x"}`, "collection-key")
	r.Header.Del("Authorization")
	w := httptest.NewRecorder()
	prepareTaskDraftCollectionHandler(client)(w, r)
	if w.Code != 401 {
		t.Fatalf("missing token: %d", w.Code)
	}
}

func TestGetCollectionHTTPReturnsCompleteOrderedItemsWithExactIDs(t *testing.T) {
	client := collectionReaderFunc(func(ctx context.Context, req *pb.GetTaskDraftRequest) (*pb.GetTaskDraftCollectionResponse, error) {
		md, _ := metadata.FromOutgoingContext(ctx)
		if req.GetRunId() != 9007199254740999 || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer user-token" {
			t.Fatalf("request %v md %v", req, md)
		}
		return validHTTPCollection(), nil
	})
	w := httptest.NewRecorder()
	getTaskDraftCollectionHandler(client)(w, collectionHTTPRequest("9007199254740999", "", false))
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	// Verify public JSON field names and exact IDs independently of private response structs.
	var envelope map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	data := envelope["data"].(map[string]any)
	items := data["items"].([]any)
	if data["run_id"] != "9007199254740999" || data["team_id"] != "9007199254740993" || data["item_count"] != float64(2) || len(items) != 2 {
		t.Fatal(data)
	}
	for i, value := range items {
		item := value.(map[string]any)
		draft := item["draft"].(map[string]any)
		if item["item_index"] != float64(i) || item["task_id"] != "0" || draft["revision"] != "9007199254740993" || draft["assignee_id"] != "9007199254740995" || draft["source_message_id"] != "9007199254740997" || len(draft["deadline"].(map[string]any)) != 9 {
			t.Fatal(item)
		}
	}
}

func TestCollectionHTTPRejectsNilServiceResults(t *testing.T) {
	preparer := collectionPreparerFunc(func(context.Context, *pb.PrepareTaskDraftRequest) (*pb.PrepareTaskDraftResponse, error) {
		return nil, nil
	})
	reader := collectionReaderFunc(func(context.Context, *pb.GetTaskDraftRequest) (*pb.GetTaskDraftCollectionResponse, error) {
		return nil, nil
	})
	itemReader := collectionItemReaderFunc(func(context.Context, *pb.GetTaskDraftItemRequest) (*pb.GetTaskDraftItemResponse, error) {
		return nil, nil
	})
	for _, call := range []func(*httptest.ResponseRecorder){
		func(w *httptest.ResponseRecorder) {
			prepareTaskDraftCollectionHandler(preparer)(w, prepareDraftHTTPRequest("2", "3", `{"instruction":"x"}`, "key"))
		},
		func(w *httptest.ResponseRecorder) {
			getTaskDraftCollectionHandler(reader)(w, collectionHTTPRequest("1", "", false))
		},
		func(w *httptest.ResponseRecorder) {
			getTaskDraftItemHandler(itemReader)(w, collectionHTTPRequest("1", "0", true))
		},
	} {
		w := httptest.NewRecorder()
		call(w)
		if w.Code != 502 {
			t.Fatalf("nil result: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestCollectionHTTPAcceptsOneAndFiveItemsAndNotStartedReply(t *testing.T) {
	for _, count := range []int32{1, 5} {
		result := validHTTPCollection()
		result.ItemCount = count
		result.Items = nil
		for i := int32(0); i < count; i++ {
			item := validHTTPCollection().Items[0]
			index := i
			item.ItemIndex = &index
			item.ReplyStatus = "not_started"
			result.Items = append(result.Items, item)
		}
		reader := collectionReaderFunc(func(context.Context, *pb.GetTaskDraftRequest) (*pb.GetTaskDraftCollectionResponse, error) {
			return result, nil
		})
		w := httptest.NewRecorder()
		getTaskDraftCollectionHandler(reader)(w, collectionHTTPRequest("9007199254740999", "", false))
		if w.Code != 200 {
			t.Fatalf("count %d: %d %s", count, w.Code, w.Body.String())
		}
	}
}

func TestLegacyDraftModeConflictStaysDistinctFromUnavailable(t *testing.T) {
	for _, tc := range []struct {
		code codes.Code
		want int
	}{{codes.FailedPrecondition, 409}, {codes.Unavailable, 503}} {
		preparer := draftPreparerFunc(func(context.Context, *pb.PrepareTaskDraftRequest) (*pb.PrepareTaskDraftResponse, error) {
			return nil, status.Error(tc.code, "private mode detail")
		})
		reader := draftReaderFunc(func(context.Context, *pb.GetTaskDraftRequest) (*pb.GetTaskDraftResponse, error) {
			return nil, status.Error(tc.code, "private mode detail")
		})
		for _, call := range []func(*httptest.ResponseRecorder){
			func(w *httptest.ResponseRecorder) {
				prepareTaskDraftHandler(preparer)(w, prepareDraftHTTPRequest("2", "3", `{"instruction":"x"}`, "key"))
			},
			func(w *httptest.ResponseRecorder) { getTaskDraftHandler(reader)(w, getDraftHTTPRequest("1")) },
		} {
			w := httptest.NewRecorder()
			call(w)
			if w.Code != tc.want || strings.Contains(w.Body.String(), "private") {
				t.Fatalf("%s: %d %s", tc.code, w.Code, w.Body.String())
			}
		}
	}
}

func TestGetCollectionHTTPRejectsMalformedCollections(t *testing.T) {
	for _, mutate := range []func(*pb.GetTaskDraftCollectionResponse){
		func(r *pb.GetTaskDraftCollectionResponse) { r.RunId = 1 }, func(r *pb.GetTaskDraftCollectionResponse) { r.TeamId = 0 }, func(r *pb.GetTaskDraftCollectionResponse) { r.GroupId = -1 },
		func(r *pb.GetTaskDraftCollectionResponse) { r.ItemCount = 0 }, func(r *pb.GetTaskDraftCollectionResponse) { r.ItemCount = 6 }, func(r *pb.GetTaskDraftCollectionResponse) { r.Items = nil }, func(r *pb.GetTaskDraftCollectionResponse) { r.Items = r.Items[:1] },
		func(r *pb.GetTaskDraftCollectionResponse) { r.Items[1] = nil }, func(r *pb.GetTaskDraftCollectionResponse) { r.Items[0].ItemIndex = nil }, func(r *pb.GetTaskDraftCollectionResponse) { r.Items[0], r.Items[1] = r.Items[1], r.Items[0] },
		func(r *pb.GetTaskDraftCollectionResponse) { r.Items[1].ItemIndex = r.Items[0].ItemIndex }, func(r *pb.GetTaskDraftCollectionResponse) { v := int32(5); r.Items[1].ItemIndex = &v },
		func(r *pb.GetTaskDraftCollectionResponse) { r.Items[1].Status = "succeeded" }, func(r *pb.GetTaskDraftCollectionResponse) { r.Items[1].TaskId = 8 }, func(r *pb.GetTaskDraftCollectionResponse) { r.Items[1].ReplyStatus = "accepted" }, func(r *pb.GetTaskDraftCollectionResponse) { r.Items[1].ReplyStatus = "" }, func(r *pb.GetTaskDraftCollectionResponse) { r.Items[1].ReplyMsgId = "fake" },
		func(r *pb.GetTaskDraftCollectionResponse) { r.Items[1].Draft = nil }, func(r *pb.GetTaskDraftCollectionResponse) { r.Items[1].Draft.Revision = 0 }, func(r *pb.GetTaskDraftCollectionResponse) { r.Items[1].Draft.Title = "" }, func(r *pb.GetTaskDraftCollectionResponse) { r.Items[1].Draft.Title = " Task " }, func(r *pb.GetTaskDraftCollectionResponse) { r.Items[1].Draft.Title = strings.Repeat("x", 201) },
		func(r *pb.GetTaskDraftCollectionResponse) { r.Items[1].Draft.Description = " Notes " }, func(r *pb.GetTaskDraftCollectionResponse) { r.Items[1].Draft.Description = strings.Repeat("x", 2001) }, func(r *pb.GetTaskDraftCollectionResponse) { r.Items[1].Draft.Description = string([]byte{0xff}) },
		func(r *pb.GetTaskDraftCollectionResponse) { r.Items[1].Draft.AssigneeResolution = "" }, func(r *pb.GetTaskDraftCollectionResponse) { r.Items[1].Draft.AssigneeId = 0 }, func(r *pb.GetTaskDraftCollectionResponse) { r.Items[1].Draft.SourceMessageId = -1 }, func(r *pb.GetTaskDraftCollectionResponse) { r.Items[1].Draft.Deadline = nil }, func(r *pb.GetTaskDraftCollectionResponse) { r.Items[1].Draft.Deadline = &pb.TaskDraftDeadline{} }, func(r *pb.GetTaskDraftCollectionResponse) { r.Items[1].Draft.DueAtUnixMs = -1 },
	} {
		result := validHTTPCollection()
		mutate(result)
		client := collectionReaderFunc(func(context.Context, *pb.GetTaskDraftRequest) (*pb.GetTaskDraftCollectionResponse, error) {
			return result, nil
		})
		w := httptest.NewRecorder()
		getTaskDraftCollectionHandler(client)(w, collectionHTTPRequest("9007199254740999", "", false))
		if w.Code != 502 || strings.Contains(w.Body.String(), `"data"`) {
			t.Fatalf("bad result %v: %d %s", result, w.Code, w.Body.String())
		}
	}
}

func TestGetCollectionItemHTTPPreservesExplicitZeroAndChecksIdentity(t *testing.T) {
	for _, tc := range []struct {
		index     string
		wantIndex int32
	}{{"0", 0}, {"1", 1}} {
		client := collectionItemReaderFunc(func(ctx context.Context, req *pb.GetTaskDraftItemRequest) (*pb.GetTaskDraftItemResponse, error) {
			md, _ := metadata.FromOutgoingContext(ctx)
			if req.GetRunId() != 9007199254740999 || req.ItemIndex == nil || req.GetItemIndex() != tc.wantIndex || md.Get("authorization")[0] != "Bearer user-token" {
				t.Fatalf("request %v", req)
			}
			r := validHTTPCollection()
			return &pb.GetTaskDraftItemResponse{RunId: r.RunId, TeamId: r.TeamId, GroupId: r.GroupId, ItemCount: r.ItemCount, Item: r.Items[tc.wantIndex]}, nil
		})
		w := httptest.NewRecorder()
		getTaskDraftItemHandler(client)(w, collectionHTTPRequest("9007199254740999", tc.index, true))
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"item_index":`+tc.index) || strings.Contains(w.Body.String(), `"items"`) {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	}
}

func TestGetCollectionItemHTTPRejectsBadIndexAndResults(t *testing.T) {
	client := collectionItemReaderFunc(func(context.Context, *pb.GetTaskDraftItemRequest) (*pb.GetTaskDraftItemResponse, error) {
		t.Fatal("invalid index reached RPC")
		return nil, nil
	})
	for _, index := range []string{"", "-1", "5", "00", "+0", "2147483648", "bad"} {
		w := httptest.NewRecorder()
		getTaskDraftItemHandler(client)(w, collectionHTTPRequest("1", index, true))
		if w.Code != 400 {
			t.Fatalf("index %q: %d", index, w.Code)
		}
	}
	w := httptest.NewRecorder()
	getTaskDraftItemHandler(client)(w, collectionHTTPRequest("1", "", false))
	if w.Code != 400 {
		t.Fatalf("missing index: %d", w.Code)
	}
	for _, mutate := range []func(*pb.GetTaskDraftItemResponse){
		func(r *pb.GetTaskDraftItemResponse) { r.RunId = 1 }, func(r *pb.GetTaskDraftItemResponse) { r.TeamId = 0 }, func(r *pb.GetTaskDraftItemResponse) { r.ItemCount = 1 }, func(r *pb.GetTaskDraftItemResponse) { r.ItemCount = 6 },
		func(r *pb.GetTaskDraftItemResponse) { r.Item = nil }, func(r *pb.GetTaskDraftItemResponse) { r.Item.ItemIndex = nil }, func(r *pb.GetTaskDraftItemResponse) { v := int32(0); r.Item.ItemIndex = &v },
	} {
		collection := validHTTPCollection()
		result := &pb.GetTaskDraftItemResponse{RunId: collection.RunId, TeamId: collection.TeamId, GroupId: collection.GroupId, ItemCount: 2, Item: collection.Items[1]}
		mutate(result)
		reader := collectionItemReaderFunc(func(context.Context, *pb.GetTaskDraftItemRequest) (*pb.GetTaskDraftItemResponse, error) {
			return result, nil
		})
		w := httptest.NewRecorder()
		getTaskDraftItemHandler(reader)(w, collectionHTTPRequest("9007199254740999", "1", true))
		if w.Code != 502 {
			t.Fatalf("bad result %v: %d", result, w.Code)
		}
	}
}

func TestCollectionHTTPMapsRPCFailuresAndRejectsBadRunOrToken(t *testing.T) {
	for _, tc := range []struct {
		code codes.Code
		want int
	}{{codes.FailedPrecondition, 409}, {codes.AlreadyExists, 409}, {codes.PermissionDenied, 403}, {codes.Unauthenticated, 401}, {codes.NotFound, 404}, {codes.InvalidArgument, 400}, {codes.Unavailable, 503}, {codes.DeadlineExceeded, 504}, {codes.Internal, 502}} {
		err := status.Error(tc.code, "private details")
		preparer := collectionPreparerFunc(func(context.Context, *pb.PrepareTaskDraftRequest) (*pb.PrepareTaskDraftResponse, error) {
			return nil, err
		})
		reader := collectionReaderFunc(func(context.Context, *pb.GetTaskDraftRequest) (*pb.GetTaskDraftCollectionResponse, error) {
			return nil, err
		})
		itemReader := collectionItemReaderFunc(func(context.Context, *pb.GetTaskDraftItemRequest) (*pb.GetTaskDraftItemResponse, error) {
			return nil, err
		})
		for _, call := range []func(*httptest.ResponseRecorder){
			func(w *httptest.ResponseRecorder) {
				prepareTaskDraftCollectionHandler(preparer)(w, prepareDraftHTTPRequest("2", "3", `{"instruction":"x"}`, "key"))
			},
			func(w *httptest.ResponseRecorder) {
				getTaskDraftCollectionHandler(reader)(w, collectionHTTPRequest("1", "", false))
			},
			func(w *httptest.ResponseRecorder) {
				getTaskDraftItemHandler(itemReader)(w, collectionHTTPRequest("1", "0", true))
			},
		} {
			w := httptest.NewRecorder()
			call(w)
			if w.Code != tc.want || strings.Contains(w.Body.String(), "private") {
				t.Fatalf("%s: %d %s", tc.code, w.Code, w.Body.String())
			}
		}
	}
	reader := collectionReaderFunc(func(context.Context, *pb.GetTaskDraftRequest) (*pb.GetTaskDraftCollectionResponse, error) {
		t.Fatal("bad request reached RPC")
		return nil, nil
	})
	itemReader := collectionItemReaderFunc(func(context.Context, *pb.GetTaskDraftItemRequest) (*pb.GetTaskDraftItemResponse, error) {
		t.Fatal("bad request reached RPC")
		return nil, nil
	})
	for _, id := range []string{"0", "01", "+1", "9223372036854775808"} {
		w := httptest.NewRecorder()
		getTaskDraftCollectionHandler(reader)(w, collectionHTTPRequest(id, "", false))
		if w.Code != 400 {
			t.Fatalf("ID %s: %d", id, w.Code)
		}
		w = httptest.NewRecorder()
		getTaskDraftItemHandler(itemReader)(w, collectionHTTPRequest(id, "0", true))
		if w.Code != 400 {
			t.Fatalf("item ID %s: %d", id, w.Code)
		}
	}
	r := collectionHTTPRequest("1", "0", true)
	r.Header.Del("Authorization")
	w := httptest.NewRecorder()
	getTaskDraftCollectionHandler(reader)(w, r)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	w = httptest.NewRecorder()
	getTaskDraftItemHandler(itemReader)(w, r)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
}
