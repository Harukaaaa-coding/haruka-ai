package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const (
	weatherAPIBaseURL       = "https://wttr.in"
	weatherRequestTimeout   = 10 * time.Second
	maxWeatherResponseBytes = 1 << 20
	maxWeatherCityRunes     = 128
)

//wttr.in JSON 响应结构

type WttrResponse struct {
	CurrentCondition []struct {
		TempC         string `json:"temp_C"`
		Humidity      string `json:"humidity"`
		WindspeedKmph string `json:"windspeedKmph"`
		WeatherDesc   []struct {
			Value string `json:"value"`
		} `json:"weatherDesc"`
	} `json:"current_condition"`

	NearestArea []struct {
		AreaName []struct {
			Value string `json:"value"`
		} `json:"areaName"`
	} `json:"nearest_area"`
}

//统一对外天气结构

type WeatherResponse struct {
	Location    string  `json:"location"`
	Temperature float64 `json:"temperature"`
	Condition   string  `json:"condition"`
	Humidity    int     `json:"humidity"`
	WindSpeed   float64 `json:"windSpeed"`
}

// WeatherAPIClient is a bounded client for the public wttr.in endpoint.
// Keeping the client on the struct allows tests and future deployments to
// provide a shared transport without losing the request-level timeout.
type WeatherAPIClient struct {
	client  *http.Client
	baseURL string
}

type WeatherHTTPError struct {
	StatusCode int
}

func (e *WeatherHTTPError) Error() string {
	return fmt.Sprintf("weather service returned HTTP status %d", e.StatusCode)
}

func NewWeatherAPIClient() *WeatherAPIClient {
	return newWeatherAPIClient(&http.Client{Timeout: weatherRequestTimeout}, weatherAPIBaseURL)
}

func newWeatherAPIClient(client *http.Client, baseURL string) *WeatherAPIClient {
	if client == nil {
		client = &http.Client{Timeout: weatherRequestTimeout}
	}
	return &WeatherAPIClient{
		client:  client,
		baseURL: strings.TrimSpace(baseURL),
	}
}

func (c *WeatherAPIClient) GetWeather(ctx context.Context, city string) (*WeatherResponse, error) {
	city = strings.TrimSpace(city)
	if city == "" || !utf8.ValidString(city) || utf8.RuneCountInString(city) > maxWeatherCityRunes {
		return nil, fmt.Errorf("invalid city")
	}
	apiURL, err := weatherURL(c.baseURL, city)
	if err != nil {
		return nil, err
	}

	if ctx == nil {
		ctx = context.Background()
	}
	requestContext, cancel := context.WithTimeout(ctx, weatherRequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(requestContext, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request failed: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		// Do not parse or surface arbitrary upstream HTML/error bodies. Drain a
		// small bounded portion so pooled transports can reuse the connection.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return nil, &WeatherHTTPError{StatusCode: resp.StatusCode}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxWeatherResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read response failed: %w", err)
	}
	if len(body) > maxWeatherResponseBytes {
		return nil, fmt.Errorf("weather response exceeds %d bytes", maxWeatherResponseBytes)
	}

	var wttrResp WttrResponse
	if err := json.Unmarshal(body, &wttrResp); err != nil {
		return nil, fmt.Errorf("json parse failed: %w", err)
	}

	if len(wttrResp.CurrentCondition) == 0 {
		return nil, fmt.Errorf("no weather data")
	}

	cc := wttrResp.CurrentCondition[0]

	temp, err := strconv.ParseFloat(cc.TempC, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid weather temperature: %w", err)
	}
	humidity, err := strconv.Atoi(cc.Humidity)
	if err != nil {
		return nil, fmt.Errorf("invalid weather humidity: %w", err)
	}
	if humidity < 0 || humidity > 100 {
		return nil, fmt.Errorf("invalid weather humidity")
	}
	wind, err := strconv.ParseFloat(cc.WindspeedKmph, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid weather wind speed: %w", err)
	}

	location := city
	if len(wttrResp.NearestArea) > 0 &&
		len(wttrResp.NearestArea[0].AreaName) > 0 {
		location = wttrResp.NearestArea[0].AreaName[0].Value
	}

	condition := "未知"
	if len(cc.WeatherDesc) > 0 {
		condition = cc.WeatherDesc[0].Value
	}

	return &WeatherResponse{
		Location:    location,
		Temperature: temp,
		Condition:   condition,
		Humidity:    humidity,
		WindSpeed:   wind,
	}, nil
}

func weatherURL(baseURL, city string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid weather API URL")
	}
	basePath := strings.TrimRight(parsed.Path, "/")
	escapedBasePath := strings.TrimRight(parsed.EscapedPath(), "/")
	parsed.Path = basePath + "/" + city
	parsed.RawPath = escapedBasePath + "/" + url.PathEscape(city)
	query := parsed.Query()
	query.Set("format", "j1")
	query.Set("lang", "zh")
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func isWeatherHTTPError(err error, statusCode int) bool {
	var responseError *WeatherHTTPError
	return errors.As(err, &responseError) && responseError.StatusCode == statusCode
}

/*
	========================
	MCP Server
	========================
*/

func NewMCPServer() *server.MCPServer {
	weatherClient := NewWeatherAPIClient()

	mcpServer := server.NewMCPServer(
		"weather-query-server",
		"1.0.0",
		server.WithToolCapabilities(true),
		server.WithLogging(),
	)

	mcpServer.AddTool(
		mcp.NewTool(
			"get_weather",
			mcp.WithDescription("获取指定城市的天气信息"),
			mcp.WithString(
				"city",
				mcp.Description("城市名称，如 Beijing、上海"),
				mcp.Required(),
			),
		),
		func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := request.GetArguments()
			city, ok := args["city"].(string)
			if !ok || city == "" {
				return nil, fmt.Errorf("invalid city argument")
			}

			weather, err := weatherClient.GetWeather(ctx, city)
			if err != nil {
				return nil, err
			}

			resultText := fmt.Sprintf(
				"城市: %s\n温度: %.1f°C\n天气: %s\n湿度: %d%%\n风速: %.1f km/h",
				weather.Location,
				weather.Temperature,
				weather.Condition,
				weather.Humidity,
				weather.WindSpeed,
			)

			return &mcp.CallToolResult{
				Content: []mcp.Content{
					mcp.TextContent{
						Type: "text",
						Text: resultText,
					},
				},
			}, nil
		},
	)

	return mcpServer
}

// StartServer 启动MCP服务器
// httpAddr: HTTP服务器监听的地址（例如":8080"）
func StartServer(httpAddr string) error {
	mcpServer := NewMCPServer()

	httpServer := server.NewStreamableHTTPServer(mcpServer)
	log.Printf("HTTP MCP server listening on %s/mcp", httpAddr)
	return httpServer.Start(httpAddr)
}
