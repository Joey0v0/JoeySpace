package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// 在线状态键前缀
const (
	keyOnline       = "online:%d"        // online:{user_id} -> ws_rpc_addr
	keyOnlineOwner  = "online_owner:%d"  // online_owner:{user_id} -> connection lease
	keyMsgDedup     = "msg_dedup:%s"     // msg_dedup:{msg_id} -> pending:<owner>:<fingerprint> or sent:<fingerprint>
	keyGroupMembers = "group:members:%d" // group:members:{group_id} -> SET of user_ids
)

type MsgDedupState int

const (
	MsgDedupPending MsgDedupState = iota
	MsgDedupReserved
	MsgDedupSent
	MsgDedupConflict
)

// RedisRepository Redis 数据访问接口
type RedisRepository interface {
	// 在线状态
	SetOnline(ctx context.Context, userID int64, wsAddr string, ttl time.Duration) error
	GetOnline(ctx context.Context, userID int64) (string, error)
	DelOnline(ctx context.Context, userID int64) error
	SetOnlineLease(ctx context.Context, userID int64, wsAddr, lease string, ttl time.Duration) error
	RefreshOnlineLease(ctx context.Context, userID int64, lease string, ttl time.Duration) (bool, error)
	DelOnlineLease(ctx context.Context, userID int64, lease string) error

	// 消息防重
	ReserveMsg(ctx context.Context, msgID, fingerprint string, ttl time.Duration) (MsgDedupState, string, error)
	ConfirmMsg(ctx context.Context, msgID, owner, fingerprint string, ttl time.Duration) (bool, error)
	ReleaseMsg(ctx context.Context, msgID, owner string) error

	// 群成员缓存
	SetGroupMembers(ctx context.Context, groupID int64, memberIDs []int64, ttl time.Duration) error
	GetGroupMembers(ctx context.Context, groupID int64) ([]int64, error)
	DelGroupMembers(ctx context.Context, groupID int64) error
}

type redisRepository struct {
	rdb *redis.Client
}

// NewRedisRepository 创建 Redis Repository
func NewRedisRepository() RedisRepository {
	return &redisRepository{rdb: RDB}
}

func (r *redisRepository) SetOnline(ctx context.Context, userID int64, wsAddr string, ttl time.Duration) error {
	key := fmt.Sprintf(keyOnline, userID)
	return r.rdb.Set(ctx, key, wsAddr, ttl).Err()
}

func (r *redisRepository) GetOnline(ctx context.Context, userID int64) (string, error) {
	key := fmt.Sprintf(keyOnline, userID)
	val, err := r.rdb.Get(ctx, key).Result()
	if err == redis.Nil {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return val, nil
}

func (r *redisRepository) DelOnline(ctx context.Context, userID int64) error {
	key := fmt.Sprintf(keyOnline, userID)
	return r.rdb.Del(ctx, key).Err()
}

// A route and its owning connection are written atomically. Closing an older
// socket must not delete a replacement socket's route.
func (r *redisRepository) SetOnlineLease(ctx context.Context, userID int64, wsAddr, lease string, ttl time.Duration) error {
	if userID <= 0 || wsAddr == "" || lease == "" || ttl.Milliseconds() <= 0 {
		return fmt.Errorf("invalid online lease")
	}
	const script = `redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[3])
redis.call('SET', KEYS[2], ARGV[2], 'PX', ARGV[3])
return 1`
	return r.rdb.Eval(ctx, script, []string{fmt.Sprintf(keyOnline, userID), fmt.Sprintf(keyOnlineOwner, userID)}, wsAddr, lease, ttl.Milliseconds()).Err()
}

func (r *redisRepository) RefreshOnlineLease(ctx context.Context, userID int64, lease string, ttl time.Duration) (bool, error) {
	if userID <= 0 || lease == "" || ttl.Milliseconds() <= 0 {
		return false, fmt.Errorf("invalid online lease")
	}
	const script = `if redis.call('GET', KEYS[2]) ~= ARGV[1] or redis.call('EXISTS', KEYS[1]) == 0 then return 0 end
redis.call('PEXPIRE', KEYS[1], ARGV[2])
redis.call('PEXPIRE', KEYS[2], ARGV[2])
return 1`
	result, err := r.rdb.Eval(ctx, script, []string{fmt.Sprintf(keyOnline, userID), fmt.Sprintf(keyOnlineOwner, userID)}, lease, ttl.Milliseconds()).Int()
	return result == 1, err
}

func (r *redisRepository) DelOnlineLease(ctx context.Context, userID int64, lease string) error {
	if userID <= 0 || lease == "" {
		return fmt.Errorf("invalid online lease")
	}
	const script = `if redis.call('GET', KEYS[2]) == ARGV[1] then
  redis.call('DEL', KEYS[1], KEYS[2])
end
return 1`
	return r.rdb.Eval(ctx, script, []string{fmt.Sprintf(keyOnline, userID), fmt.Sprintf(keyOnlineOwner, userID)}, lease).Err()
}

func (r *redisRepository) ReserveMsg(ctx context.Context, msgID, fingerprint string, ttl time.Duration) (MsgDedupState, string, error) {
	key := fmt.Sprintf(keyMsgDedup, msgID)
	ownerBytes := make([]byte, 16)
	if _, err := rand.Read(ownerBytes); err != nil {
		return MsgDedupPending, "", err
	}
	owner := "pending:" + hex.EncodeToString(ownerBytes) + ":" + fingerprint
	reserved, err := r.rdb.SetNX(ctx, key, owner, ttl).Result()
	if err != nil {
		return MsgDedupPending, "", err
	}
	if reserved {
		return MsgDedupReserved, owner, nil
	}
	value, err := r.rdb.Get(ctx, key).Result()
	if err == redis.Nil {
		return MsgDedupPending, "", nil
	}
	if err != nil {
		return MsgDedupPending, "", err
	}
	if strings.HasPrefix(value, "sent:") {
		if value == "sent:"+fingerprint {
			return MsgDedupSent, "", nil
		}
		return MsgDedupConflict, "", nil
	}
	if strings.HasPrefix(value, "pending:") && !strings.HasSuffix(value, ":"+fingerprint) {
		return MsgDedupConflict, "", nil
	}
	return MsgDedupPending, "", nil
}

func (r *redisRepository) ConfirmMsg(ctx context.Context, msgID, owner, fingerprint string, ttl time.Duration) (bool, error) {
	key := fmt.Sprintf(keyMsgDedup, msgID)
	const script = `if redis.call('GET', KEYS[1]) == ARGV[1] then
  redis.call('PSETEX', KEYS[1], ARGV[2], ARGV[3])
  return 1
end
return 0`
	confirmed, err := r.rdb.Eval(ctx, script, []string{key}, owner, ttl.Milliseconds(), "sent:"+fingerprint).Int64()
	return confirmed == 1, err
}

func (r *redisRepository) ReleaseMsg(ctx context.Context, msgID, owner string) error {
	key := fmt.Sprintf(keyMsgDedup, msgID)
	const script = `if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0`
	return r.rdb.Eval(ctx, script, []string{key}, owner).Err()
}

func (r *redisRepository) SetGroupMembers(ctx context.Context, groupID int64, memberIDs []int64, ttl time.Duration) error {
	key := fmt.Sprintf(keyGroupMembers, groupID)
	pipe := r.rdb.Pipeline()
	// 先删除旧缓存
	pipe.Del(ctx, key)
	// 批量添加成员
	members := make([]interface{}, len(memberIDs))
	for i, id := range memberIDs {
		members[i] = id
	}
	if len(members) > 0 {
		pipe.SAdd(ctx, key, members...)
		pipe.Expire(ctx, key, ttl)
	}
	_, err := pipe.Exec(ctx)
	return err
}

func (r *redisRepository) GetGroupMembers(ctx context.Context, groupID int64) ([]int64, error) {
	key := fmt.Sprintf(keyGroupMembers, groupID)
	vals, err := r.rdb.SMembers(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	if len(vals) == 0 {
		return nil, nil // 缓存未命中
	}
	ids := make([]int64, 0, len(vals))
	for _, v := range vals {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			continue
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func (r *redisRepository) DelGroupMembers(ctx context.Context, groupID int64) error {
	key := fmt.Sprintf(keyGroupMembers, groupID)
	return r.rdb.Del(ctx, key).Err()
}
