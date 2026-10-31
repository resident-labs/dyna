//go:build !dynaeval

package main

import "github.com/gin-gonic/gin"

func proxyEvalMiddleware() (gin.HandlerFunc, func(), error) {
	return nil, func() {}, nil
}
