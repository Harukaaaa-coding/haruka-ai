package modelcatalog

import (
	"net/http"

	"GopherAI/common/modelgateway"
	"GopherAI/controller"
	"github.com/gin-gonic/gin"
)

type Response struct {
	controller.Response
	DefaultTimeoutMS int64                       `json:"defaultTimeoutMs"`
	Providers        []modelgateway.Provider     `json:"providers"`
	Models           []modelgateway.CatalogModel `json:"models"`
}

// GetModels returns only public routing metadata.  Credentials and provider
// endpoints are never retained by the registry and therefore cannot leak here.
func GetModels(c *gin.Context) {
	registry := modelgateway.GetDefaultRegistry()
	response := &Response{
		DefaultTimeoutMS: registry.Timeout().Milliseconds(),
		Providers:        registry.Providers(),
		Models:           registry.Models(),
	}
	response.Success()
	c.JSON(http.StatusOK, response)
}
