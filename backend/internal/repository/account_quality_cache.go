package repository

import (
	"context"
	"fmt"
)

func (c *tempUnschedCache) SetAccountBalanceBlock(ctx context.Context, id, generation int64) error {
	return c.rdb.Set(ctx, fmt.Sprintf("account_quality:{%d}:%d:balance_block", id, generation), "1", 0).Err()
}

func (c *tempUnschedCache) HasAccountBalanceBlock(ctx context.Context, id, generation int64) (bool, error) {
	n, err := c.rdb.Exists(ctx, fmt.Sprintf("account_quality:{%d}:%d:balance_block", id, generation)).Result()
	return n > 0, err
}
