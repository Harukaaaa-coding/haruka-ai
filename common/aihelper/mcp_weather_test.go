package aihelper

import "testing"

func TestWeatherToolCallFromQuery(t *testing.T) {
	tests := []struct {
		name  string
		query string
		city  string
		ok    bool
	}{
		{
			name:  "explicit Chinese tool request",
			query: "请调用 get_weather 工具查询上海当前天气，并告诉我温度和湿度。",
			city:  "上海",
			ok:    true,
		},
		{
			name:  "direct Chinese weather request",
			query: "请问北京市今天的天气怎么样？",
			city:  "北京",
			ok:    true,
		},
		{
			name:  "English city after weather",
			query: "What is the weather in Shanghai today?",
			city:  "Shanghai",
			ok:    true,
		},
		{
			name:  "English city before weather",
			query: "Tell me the New York weather.",
			city:  "New York",
			ok:    true,
		},
		{
			name:  "weather without a city",
			query: "今天天气怎么样？",
			ok:    false,
		},
		{
			name:  "non-weather question",
			query: "请介绍一下上海。",
			ok:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toolCall, ok := weatherToolCallFromQuery(tt.query)
			if ok != tt.ok {
				t.Fatalf("weatherToolCallFromQuery() ok = %v, want %v", ok, tt.ok)
			}
			if !tt.ok {
				return
			}
			if toolCall.ToolName != "weather.get_weather" {
				t.Fatalf("tool name = %q, want weather.get_weather", toolCall.ToolName)
			}
			if got, _ := toolCall.Args["city"].(string); got != tt.city {
				t.Fatalf("city = %q, want %q", got, tt.city)
			}
		})
	}
}
