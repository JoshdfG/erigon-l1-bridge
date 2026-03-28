package arbitrum

import "github.com/erigontech/erigon-l1-bridge/internal/entity"

type Client struct{}
func (c *Client) ChainID() entity.ChainID { return entity.ChainIDArbitrum }
