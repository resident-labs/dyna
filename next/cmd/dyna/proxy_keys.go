package main

import (
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/model"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"golang.org/x/crypto/bcrypt"
)

type proxyKeyRequest struct {
	ID       string                   `json:"id"`
	Provider contract.AccountProvider `json:"provider"`
	Key      string                   `json:"key"`
}

func proxyKeyRoutes(router *gin.Engine, service **cliproxy.Service, managementHash string) {
	router.POST("/dyna/keys", func(c *gin.Context) {
		password := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
		if bcrypt.CompareHashAndPassword([]byte(managementHash), []byte(password)) != nil {
			c.Status(http.StatusUnauthorized)
			return
		}
		var req proxyKeyRequest
		if err := c.ShouldBindJSON(&req); err != nil || !strings.HasPrefix(req.ID, "dyna-key-") || len(req.ID) != len("dyna-key-")+32 || strings.TrimSpace(req.Key) == "" || len(req.Key) > 4096 {
			c.Status(http.StatusBadRequest)
			return
		}
		if _, err := hex.DecodeString(strings.TrimPrefix(req.ID, "dyna-key-")); err != nil {
			c.Status(http.StatusBadRequest)
			return
		}
		for _, provider := range model.KeyProviders() {
			if provider.Provider != req.Provider {
				continue
			}
			name := string(provider.Provider)
			// The proxy SDK defines provider metadata as map[string]any.
			auth := &coreauth.Auth{
				ID:         req.ID,
				Provider:   "openai-compatible-" + name,
				Label:      name,
				Status:     coreauth.StatusActive,
				Attributes: map[string]string{"api_key": req.Key, "auth_kind": "apikey", "base_url": provider.URL, "compat_name": name, "provider_key": "openai-compatible-" + name},
				Metadata:   map[string]any{"type": "openai-compatibility"},
			}
			if err := (*service).RegisterDynaCredential(c.Request.Context(), auth, provider.Models[0]); err != nil {
				c.Status(http.StatusServiceUnavailable)
				return
			}
			c.JSON(http.StatusOK, gin.H{"status": "ok"})
			return
		}
		c.Status(http.StatusBadRequest)
	})
	router.DELETE("/dyna/keys/:id", func(c *gin.Context) {
		password := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
		if bcrypt.CompareHashAndPassword([]byte(managementHash), []byte(password)) != nil {
			c.Status(http.StatusUnauthorized)
			return
		}
		id := c.Param("id")
		if !strings.HasPrefix(id, "dyna-key-") || len(id) != len("dyna-key-")+32 {
			c.Status(http.StatusBadRequest)
			return
		}
		if _, err := hex.DecodeString(strings.TrimPrefix(id, "dyna-key-")); err != nil {
			c.Status(http.StatusBadRequest)
			return
		}
		if err := (*service).DeleteDynaCredential(c.Request.Context(), id); err != nil {
			c.Status(http.StatusServiceUnavailable)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
}
