package snowflake

import (
	"fmt"

	sf "github.com/bwmarrin/snowflake"
)

var node *sf.Node

// Init 初始化 Snowflake 节点
func Init(serverID int64) error {
	var err error
	node, err = sf.NewNode(serverID)
	if err != nil {
		return fmt.Errorf("init snowflake node failed: %w", err)
	}
	return nil
}

// GenID 生成唯一 ID
func GenID() int64 {
	return node.Generate().Int64()
}
