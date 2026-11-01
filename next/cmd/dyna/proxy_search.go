package main

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
)

func proxySearchRoutes(router *gin.Engine, service **cliproxy.Service) {
	router.POST("/dyna/search", func(c *gin.Context) {
		if *service == nil {
			c.AbortWithStatus(http.StatusServiceUnavailable)
			return
		}
		(*service).DynaSearchAuth(c)
	}, func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
		var req struct {
			Provider string `json:"provider"`
			Query    string `json:"query"`
			Limit    int    `json:"limit"`
		}
		values, present := c.Request.Header[http.CanonicalHeaderKey("X-Dyna-Eligible-Auth")]
		c.Request.Header.Del("X-Dyna-Eligible-Auth")
		if !present || c.ShouldBindJSON(&req) != nil || (req.Provider != "kimi" && req.Provider != "zai") || strings.TrimSpace(req.Query) == "" || req.Limit < 1 || req.Limit > 10 {
			c.JSON(http.StatusBadRequest, struct {
				Error string `json:"error"`
			}{"invalid search request"})
			return
		}
		results, err := (*service).DynaSearch(c.Request.Context(), req.Provider, req.Query, req.Limit, values)
		if err != nil {
			status := http.StatusBadGateway
			var refusal interface{ StatusCode() int }
			if errors.Is(err, cliproxy.ErrNotSignedIn) {
				status = http.StatusUnauthorized
			} else if errors.As(err, &refusal) && (refusal.StatusCode() == http.StatusUnauthorized || refusal.StatusCode() == http.StatusForbidden) {
				status = refusal.StatusCode()
			}
			c.JSON(status, struct {
				Error string `json:"error"`
			}{err.Error()})
			return
		}
		c.JSON(http.StatusOK, struct {
			Results []cliproxy.Result `json:"results"`
		}{results})
	})
}
