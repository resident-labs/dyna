package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/model"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"golang.org/x/crypto/bcrypt"
)

type proxyKeyRequest struct {
	ID          string                   `json:"id"`
	Provider    contract.AccountProvider `json:"provider"`
	Key         string                   `json:"key"`
	DisplayName string                   `json:"displayName"`
	BaseURL     string                   `json:"baseUrl"`
	Models      []string                 `json:"models"`
	Discover    bool                     `json:"discover"`
}

// modelDiscovery is a variable so tests never reach a provider. A failed list leaves the
// owner to enter model IDs by hand, so its error, which can hold the URL, stays here.
var modelDiscovery = &http.Client{Timeout: 10 * time.Second}

// discoverModels reads a provider's OpenAI-style model list with the key being saved.
func discoverModels(ctx context.Context, baseURL, key string) []string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/models", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	res, err := modelDiscovery.Do(req)
	if err != nil {
		return nil
	}
	defer res.Body.Close()
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if res.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&list) != nil {
		return nil
	}
	models := make([]string, 0, len(list.Data))
	for _, entry := range list.Data {
		models = append(models, entry.ID)
	}
	return models
}

// mergeModels keeps seeded IDs first, so the proxy's activation check does not depend on the list.
func mergeModels(seed, discovered []string) []string {
	merged := slices.Clone(seed)
	for _, id := range discovered {
		if id != "" && !strings.ContainsAny(id, ", \t\r\n") && !slices.Contains(merged, id) {
			merged = append(merged, id)
		}
	}
	return merged
}

func validProxyKeyID(id string) bool {
	if !strings.HasPrefix(id, "dyna-key-") || len(id) != len("dyna-key-")+32 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(id, "dyna-key-"))
	return err == nil
}

type oauthImportToken struct {
	Access    string `json:"access_token"`
	Refresh   string `json:"refresh_token"`
	Domain    string `json:"domain"`
	DeviceID  string `json:"device_id"`
	Email     string `json:"email"`
	AccountID string `json:"account_id"`
}

func validOAuthIdentifier(value string) bool {
	return value != "" && len(value) <= 320 && !strings.ContainsFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
}

// validOAuthImport checks the fields each provider's proxy executor depends on.
func validOAuthImport(provider contract.AccountProvider, token oauthImportToken) bool {
	email := token.Email == "" || (strings.Contains(token.Email, "@") && validOAuthIdentifier(token.Email))
	switch provider {
	case contract.ProviderAnthropic:
		// The Claude executor sends any other token as an API key.
		return strings.HasPrefix(token.Access, "sk-ant-oat") && email && token.AccountID == ""
	case contract.ProviderOpenAI:
		// The Codex executor sends this as Chatgpt-Account-Id. Without an
		// id_token, nothing else supplies it before the first refresh.
		return validOAuthIdentifier(token.AccountID) && email
	case contract.ProviderMeta:
		return email && token.AccountID == ""
	default:
		return token.Email == "" && token.AccountID == ""
	}
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
		if err := c.ShouldBindJSON(&req); err != nil || !validProxyKeyID(req.ID) || req.Provider == "" || strings.TrimSpace(req.Key) == "" || len(req.Key) > 4096 {
			c.Status(http.StatusBadRequest)
			return
		}
		auth := &coreauth.Auth{ID: req.ID, Status: coreauth.StatusActive, Label: string(req.Provider)}
		first := model.MetaKeyModel
		if req.Provider == contract.ProviderMeta {
			auth.Provider = "meta"
			auth.Attributes = map[string]string{"api_key": req.Key, "auth_kind": "apikey"}
			auth.Metadata = map[string]any{"type": "meta"}
		} else {
			models := req.Models
			if req.Discover {
				models = mergeModels(models, discoverModels(c.Request.Context(), req.BaseURL, req.Key))
			}
			if len(models) == 0 || req.BaseURL == "" {
				c.Status(http.StatusUnprocessableEntity)
				return
			}
			name := string(req.Provider)
			auth.Provider = "openai-compatible-" + name
			auth.Attributes = map[string]string{"api_key": req.Key, "auth_kind": "apikey", "base_url": req.BaseURL, "compat_name": name, "provider_key": auth.Provider, cliproxy.DynaModelsAttribute: strings.Join(models, ",")}
			auth.Metadata = map[string]any{"type": "openai-compatibility"}
			first = models[0]
			if model.IsCustomProxyProvider(auth.Provider) {
				auth.Attributes[cliproxy.DynaDisplayNameAttribute] = req.DisplayName
				auth.Prefix = name
				first = name + "/" + first
			}
		}
		if (*service).RegisterDynaCredential(c.Request.Context(), auth) != nil {
			c.Status(http.StatusServiceUnavailable)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "model": first})
	})

	dyna.POST("/oauth/import", func(c *gin.Context) {
		var req struct {
			ID         string                   `json:"id"`
			Provider   contract.AccountProvider `json:"provider"`
			ExpiresAt  time.Time                `json:"expiresAt"`
			Credential string                   `json:"credential"`
		}
		if c.ShouldBindJSON(&req) != nil || !strings.HasPrefix(req.ID, "dyna-oauth-") || !validProxyKeyID("dyna-key-"+strings.TrimPrefix(req.ID, "dyna-oauth-")) || req.ExpiresAt.Unix() <= 0 || len(req.Credential) > 16384 {
			c.Status(http.StatusBadRequest)
			return
		}
		provider, ok := model.OAuthProxyProvider(req.Provider)
		if !ok {
			c.Status(http.StatusBadRequest)
			return
		}
		var token oauthImportToken
		if json.Unmarshal([]byte(req.Credential), &token) != nil || strings.TrimSpace(token.Access) == "" || strings.TrimSpace(token.Refresh) == "" || !validOAuthImport(req.Provider, token) {
			c.Status(http.StatusBadRequest)
			return
		}
		if token.Domain != "" && (req.Provider != contract.ProviderKimi || (token.Domain != "kimi.com" && token.Domain != "kimi.ai")) {
			c.Status(http.StatusBadRequest)
			return
		}
		// The proxy SDK uses provider-specific metadata maps for credentials.
		auth := &coreauth.Auth{ID: req.ID, Provider: provider, Label: provider, Status: coreauth.StatusActive, Metadata: map[string]any{"type": provider, "auth_kind": "oauth", "access_token": token.Access, "refresh_token": token.Refresh, "expired": req.ExpiresAt.UTC().Format(time.RFC3339Nano), "token_type": "Bearer"}}
		if req.Provider == contract.ProviderMeta {
			// Meta has no refresh grant. The identity token mints a new key after a 401,
			// and an expiry here would stop the proxy selecting the credential first.
			delete(auth.Metadata, "refresh_token")
			delete(auth.Metadata, "expired")
			auth.Metadata["dca_token"] = token.Refresh
		}
		if token.Domain != "" {
			auth.Metadata["domain"] = token.Domain
		}
		if req.Provider == contract.ProviderKimi && token.DeviceID != "" {
			auth.Metadata["device_id"] = token.DeviceID
		}
		if token.Email != "" {
			auth.Metadata["email"] = token.Email
		}
		if token.AccountID != "" {
			auth.Metadata["account_id"] = token.AccountID
		}
		if (*service).RegisterDynaCredential(c.Request.Context(), auth) != nil {
			c.Status(http.StatusServiceUnavailable)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
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
			ID    string `json:"id"`
			Model string `json:"model"`
		}
		if c.ShouldBindJSON(&req) != nil || req.ID == "" {
			c.Status(http.StatusBadRequest)
			return
		}
		bound, err := credentials.bound(req.ID)
		if err != nil || !bound || (*service).ActivateDynaCredential(c.Request.Context(), req.ID, req.Model) != nil {
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
