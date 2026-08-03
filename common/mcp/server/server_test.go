package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const weatherJSON = `{
  "current_condition": [{
    "temp_C": "23.5",
    "humidity": "71",
    "windspeedKmph": "12.3",
    "weatherDesc": [{"value": "Cloudy"}]
  }],
  "nearest_area": [{"areaName": [{"value": "Test City"}]}]
}`

func TestWeatherAPIClientUsesEscapedPathAndBoundedRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", request.Method)
		}
		if request.URL.EscapedPath() != "/New%20York%2FQueens" {
			t.Fatalf("escaped path = %q", request.URL.EscapedPath())
		}
		if request.URL.Query().Get("format") != "j1" || request.URL.Query().Get("lang") != "zh" {
			t.Fatalf("unexpected query: %s", request.URL.RawQuery)
		}
		if request.Header.Get("Accept") != "application/json" {
			t.Fatalf("Accept = %q", request.Header.Get("Accept"))
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(weatherJSON))
	}))
	defer server.Close()

	client := newWeatherAPIClient(server.Client(), server.URL)
	weather, err := client.GetWeather(context.Background(), " New York/Queens ")
	if err != nil {
		t.Fatalf("GetWeather() error = %v", err)
	}
	if weather.Location != "Test City" || weather.Temperature != 23.5 || weather.Humidity != 71 || weather.WindSpeed != 12.3 || weather.Condition != "Cloudy" {
		t.Fatalf("unexpected weather response: %#v", weather)
	}
}

func TestWeatherAPIClientRejectsNonSuccessStatusWithoutParsingBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusTooManyRequests)
		_, _ = writer.Write([]byte("<html>upstream error</html>"))
	}))
	defer server.Close()

	_, err := newWeatherAPIClient(server.Client(), server.URL).GetWeather(context.Background(), "Beijing")
	if !isWeatherHTTPError(err, http.StatusTooManyRequests) {
		t.Fatalf("GetWeather() error = %v, want typed HTTP status error", err)
	}
}

func TestWeatherAPIClientHonorsCallerCancellation(t *testing.T) {
	requestStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(requestStarted)
		<-request.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, err := newWeatherAPIClient(server.Client(), server.URL).GetWeather(ctx, "Beijing")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("GetWeather() error = %v, want caller deadline", err)
	}
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("weather request did not reach server")
	}
}

func TestWeatherAPIClientRejectsOversizedAndMalformedResponses(t *testing.T) {
	t.Run("oversized", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write([]byte(strings.Repeat("x", maxWeatherResponseBytes+1)))
		}))
		defer server.Close()
		_, err := newWeatherAPIClient(server.Client(), server.URL).GetWeather(context.Background(), "Beijing")
		if err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("GetWeather() error = %v, want response-size error", err)
		}
	})

	t.Run("invalid numeric fields", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write([]byte(`{"current_condition":[{"temp_C":"not-a-number","humidity":"50","windspeedKmph":"4"}]}`))
		}))
		defer server.Close()
		_, err := newWeatherAPIClient(server.Client(), server.URL).GetWeather(context.Background(), "Beijing")
		if err == nil || !strings.Contains(err.Error(), "temperature") {
			t.Fatalf("GetWeather() error = %v, want numeric validation error", err)
		}
	})
}

func TestWeatherURLRejectsInvalidEndpointAndCity(t *testing.T) {
	if _, err := weatherURL("not a URL", "Beijing"); err == nil {
		t.Fatal("invalid endpoint was accepted")
	}
	client := newWeatherAPIClient(nil, weatherAPIBaseURL)
	for _, city := range []string{"", string([]byte{0xff}), strings.Repeat("a", maxWeatherCityRunes+1)} {
		if _, err := client.GetWeather(context.Background(), city); err == nil {
			t.Fatalf("invalid city %q was accepted", city)
		}
	}
}
