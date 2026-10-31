package main

import (
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/resident-labs/dyna/contract"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"golang.org/x/crypto/bcrypt"
)

type proxyKeyRequest struct {
	ID       string                   `json:"id"`
	Provider contract.AccountProvider `json:"provider"`
	Key      string                   `json:"key"`
}

func keyAuth(provider config.OpenAICompatibility, id, key string) *coreauth.Auth {
	name := provider.Name
	// The proxy SDK defines credential metadata as map[string]any.
	return &coreauth.Auth{
		ID: id, Provider: "openai-compatible-" + name, Label: name,
		Prefix: name, Status: coreauth.StatusActive,
		Attributes: map[string]string{"api_key": key, "auth_kind": "apikey", "base_url": provider.BaseURL, "compat_name": name, "provider_key": "openai-compatible-" + name, "dyna_provider": name},
		Metadata:   map[string]any{"type": "openai-compatibility"},
	}
}

func validProxyKeyID(id string) bool {
	if !strings.HasPrefix(id, "dyna-key-") || len(id) != len("dyna-key-")+32 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(id, "dyna-key-"))
	return err == nil
}

func proxyKeyRoutes(router *gin.Engine, service **cliproxy.Service, credentials *proxyStore, managementHash string) {
	dyna := router.Group("/dyna", func(c *gin.Context) {
		password := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
		if bcrypt.CompareHashAndPassword([]byte(managementHash), []byte(password)) != nil {
			c.AbortWithStatus(http.StatusUnauthorized)
		}
	})
	dyna.POST("/keys", func(c *gin.Context) {
		var req proxyKeyRequest
		if err := c.ShouldBindJSON(&req); err != nil || !validProxyKeyID(req.ID) || strings.TrimSpace(req.Key) == "" || len(req.Key) > 4096 {
			c.Status(http.StatusBadRequest)
			return
		}
		for _, provider := range credentials.catalog {
			if provider.Name != string(req.Provider) {
				continue
			}
			auth := keyAuth(provider, req.ID, req.Key)
			if err := (*service).RegisterDynaCredential(c.Request.Context(), auth); err != nil {
				c.Status(http.StatusServiceUnavailable)
				return
			}
			c.JSON(http.StatusOK, gin.H{"status": "ok"})
			return
		}
		c.Status(http.StatusBadRequest)
	})
	dyna.DELETE("/keys/:id", func(c *gin.Context) {
		id := c.Param("id")
		if !validProxyKeyID(id) {
			c.Status(http.StatusBadRequest)
			return
		}
		if err := (*service).DeleteDynaCredential(c.Request.Context(), id); err != nil {
			c.Status(http.StatusServiceUnavailable)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	dyna.GET("/credentials", func(c *gin.Context) {
		c.JSON(http.StatusOK, struct {
			Credentials []cliproxy.DynaCredential `json:"credentials"`
		}{(*service).DynaCredentials()})
	})
	dyna.POST("/credentials/activate", func(c *gin.Context) {
		var req struct {
			ID     string `json:"id"`
			Model  string `json:"model"`
			Prefix string `json:"prefix"`
		}
		if c.ShouldBindJSON(&req) != nil || req.ID == "" {
			c.Status(http.StatusBadRequest)
			return
		}
		bound, err := credentials.bound(req.ID)
		if err != nil || !bound || (*service).ActivateDynaCredential(c.Request.Context(), req.ID, req.Model, req.Prefix) != nil {
			c.Status(http.StatusServiceUnavailable)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	dyna.POST("/credentials/remove", func(c *gin.Context) {
		var req struct {
			ID string `json:"id"`
		}
		if c.ShouldBindJSON(&req) != nil || req.ID == "" {
			c.Status(http.StatusBadRequest)
			return
		}
		if (*service).DeleteDynaCredential(c.Request.Context(), req.ID) != nil {
			c.Status(http.StatusServiceUnavailable)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
}
