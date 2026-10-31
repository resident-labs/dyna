package agent

import (
	"context"
	"io"
	"net/http"
	"strconv"

	"github.com/resident-labs/dyna/box/computer"
)

func (c *Client) watchComputer(ctx context.Context, generation uint64) {
	reader, writer := io.Pipe()
	defer reader.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://dyna/v1/computers/frames?generation="+strconv.FormatUint(generation, 10), reader)
	if err != nil {
		writer.CloseWithError(err)
		return
	}
	go func() {
		resp, err := c.http.Do(req)
		if err != nil {
			reader.CloseWithError(err)
			return
		}
		resp.Body.Close()
		reader.Close()
	}()
	err = computer.Watch(ctx, writer)
	writer.CloseWithError(err)
}
