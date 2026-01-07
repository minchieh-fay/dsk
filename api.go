package dsk

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	BaseURL = "https://chat.deepseek.com/api/v0"
)

// DeepSeekAPI DeepSeek API 客户端
type DeepSeekAPI struct {
	authToken string
	powSolver *DeepSeekPOW
	client    *http.Client
}

// NewDeepSeekAPI 创建新的 API 客户端
// WASM 文件已嵌入到二进制中，无需指定路径
func NewDeepSeekAPI(authToken string) (*DeepSeekAPI, error) {
	if authToken == "" {
		return nil, fmt.Errorf("auth token cannot be empty")
	}

	// 使用嵌入的 WASM 文件
	powSolver, err := NewDeepSeekPOW("")
	if err != nil {
		return nil, fmt.Errorf("failed to create PoW solver: %w", err)
	}

	return &DeepSeekAPI{
		authToken: authToken,
		powSolver: powSolver,
		client: &http.Client{
			Timeout: 0, // 无超时，用于长连接
		},
	}, nil
}

// NewDeepSeekAPIWithCustomWASM 使用自定义 WASM 文件创建 API 客户端
// 仅在需要测试或使用自定义 WASM 文件时使用
func NewDeepSeekAPIWithCustomWASM(authToken string, wasmPath string) (*DeepSeekAPI, error) {
	if authToken == "" {
		return nil, fmt.Errorf("auth token cannot be empty")
	}

	if wasmPath == "" {
		return nil, fmt.Errorf("wasm path cannot be empty when using custom WASM")
	}

	powSolver, err := NewDeepSeekPOW(wasmPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create PoW solver: %w", err)
	}

	return &DeepSeekAPI{
		authToken: authToken,
		powSolver: powSolver,
		client: &http.Client{
			Timeout: 0, // 无超时，用于长连接
		},
	}, nil
}

// Close 清理资源
func (api *DeepSeekAPI) Close() error {
	if api.powSolver != nil {
		return api.powSolver.Close()
	}
	return nil
}

// getHeaders 获取请求头
func (api *DeepSeekAPI) getHeaders(powResponse string) map[string]string {
	headers := map[string]string{
		"accept":            "*/*",
		"accept-language":   "en,fr-FR;q=0.9,fr;q=0.8,es-ES;q=0.7,es;q=0.6,en-US;q=0.5,am;q=0.4,de;q=0.3",
		"authorization":     fmt.Sprintf("Bearer %s", api.authToken),
		"content-type":      "application/json",
		"origin":            "https://chat.deepseek.com",
		"referer":           "https://chat.deepseek.com/",
		"user-agent":        "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/132.0.0.0 Safari/537.36",
		"x-app-version":     "20241129.1",
		"x-client-locale":   "en_US",
		"x-client-platform": "web",
		"x-client-version":  "1.0.0-always",
	}

	if powResponse != "" {
		headers["x-ds-pow-response"] = powResponse
	}

	return headers
}

// getPowChallenge 获取 PoW 挑战
func (api *DeepSeekAPI) getPowChallenge() (ChallengeConfig, error) {
	url := fmt.Sprintf("%s/chat/create_pow_challenge", BaseURL)

	reqBody := map[string]interface{}{
		"target_path": "/api/v0/chat/completion",
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return ChallengeConfig{}, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return ChallengeConfig{}, fmt.Errorf("failed to create request: %w", err)
	}

	for k, v := range api.getHeaders("") {
		req.Header.Set(k, v)
	}

	resp, err := api.client.Do(req)
	if err != nil {
		return ChallengeConfig{}, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return ChallengeConfig{}, fmt.Errorf("failed to get challenge: status %d, body: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Data struct {
			BizData struct {
				Challenge ChallengeConfig `json:"challenge"`
			} `json:"biz_data"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return ChallengeConfig{}, fmt.Errorf("failed to decode response: %w", err)
	}

	return result.Data.BizData.Challenge, nil
}

// makeRequest 发送 HTTP 请求
func (api *DeepSeekAPI) makeRequest(method, endpoint string, jsonData map[string]interface{}, powRequired bool) (map[string]interface{}, error) {
	url := fmt.Sprintf("%s%s", BaseURL, endpoint)

	var powResponse string
	if powRequired {
		challenge, err := api.getPowChallenge()
		if err != nil {
			return nil, fmt.Errorf("failed to get PoW challenge: %w", err)
		}

		powResponse, err = api.powSolver.SolveChallenge(challenge)
		if err != nil {
			return nil, fmt.Errorf("failed to solve PoW challenge: %w", err)
		}
	}

	reqBody, err := json.Marshal(jsonData)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest(method, url, bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	headers := api.getHeaders(powResponse)
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := api.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("authentication failed: invalid or expired token")
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("rate limit exceeded")
	}

	if resp.StatusCode >= http.StatusInternalServerError {
		return nil, fmt.Errorf("server error: status %d, body: %s", resp.StatusCode, string(body))
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("request failed: status %d, body: %s", resp.StatusCode, string(body))
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return result, nil
}

// CreateChatSession 创建新的聊天会话
func (api *DeepSeekAPI) CreateChatSession() (string, error) {
	resp, err := api.makeRequest("POST", "/chat_session/create", map[string]interface{}{
		"character_id": nil,
	}, false)
	if err != nil {
		return "", err
	}

	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("invalid response format: missing data")
	}

	bizData, ok := data["biz_data"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("invalid response format: missing biz_data")
	}

	id, ok := bizData["id"].(string)
	if !ok {
		return "", fmt.Errorf("invalid response format: missing id")
	}

	return id, nil
}

// Chunk 表示流式响应的一个数据块
type Chunk struct {
	Type         string `json:"type"`          // "text" 或 "thinking"
	Content      string `json:"content"`       // 内容
	MessageID    string `json:"message_id"`    // 消息 ID（如果有）
	FinishReason string `json:"finish_reason"` // 完成原因（如果有）
}

// ChatCompletion 发送消息并获取流式响应
func (api *DeepSeekAPI) ChatCompletion(chatSessionID, prompt string, parentMessageID *string, thinkingEnabled, searchEnabled bool) (<-chan Chunk, <-chan error) {
	chunkChan := make(chan Chunk, 10)
	errChan := make(chan error, 1)

	go func() {
		defer close(chunkChan)
		defer close(errChan)

		// 获取 PoW 挑战并解决
		challenge, err := api.getPowChallenge()
		if err != nil {
			errChan <- fmt.Errorf("failed to get PoW challenge: %w", err)
			return
		}

		powResponse, err := api.powSolver.SolveChallenge(challenge)
		if err != nil {
			errChan <- fmt.Errorf("failed to solve PoW challenge: %w", err)
			return
		}

		// 调试：检查 PoW 响应是否为空
		if powResponse == "" {
			errChan <- fmt.Errorf("PoW response is empty")
			return
		}

		// 准备请求体
		reqBody := map[string]interface{}{
			"chat_session_id":  chatSessionID,
			"prompt":           prompt,
			"ref_file_ids":     []interface{}{},
			"thinking_enabled": thinkingEnabled,
			"search_enabled":   searchEnabled,
		}

		if parentMessageID != nil {
			reqBody["parent_message_id"] = *parentMessageID
		}

		jsonData, err := json.Marshal(reqBody)
		if err != nil {
			errChan <- fmt.Errorf("failed to marshal request: %w", err)
			return
		}

		// 创建请求
		url := fmt.Sprintf("%s/chat/completion", BaseURL)
		req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
		if err != nil {
			errChan <- fmt.Errorf("failed to create request: %w", err)
			return
		}

		headers := api.getHeaders(powResponse)
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		// 发送请求
		debugPrint("Making POST request to: %s", url)
		debugPrint("Request headers: authorization, content-type, x-ds-pow-response")
		resp, err := api.client.Do(req)
		if err != nil {
			errChan <- fmt.Errorf("failed to make request: %w", err)
			return
		}
		defer resp.Body.Close()

		debugPrint("Response status: %d", resp.StatusCode)
		debugPrint("Content-Type: %s", resp.Header.Get("Content-Type"))

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			bodyStr := string(body)
			if len(bodyStr) > 500 {
				bodyStr = bodyStr[:500] + "..."
			}
			if resp.StatusCode == http.StatusUnauthorized {
				errChan <- fmt.Errorf("authentication failed: invalid or expired token")
			} else if resp.StatusCode == http.StatusTooManyRequests {
				errChan <- fmt.Errorf("rate limit exceeded")
			} else {
				errChan <- fmt.Errorf("request failed: status %d, body: %s", resp.StatusCode, bodyStr)
			}
			return
		}

		// 调试：检查响应内容类型
		contentType := resp.Header.Get("Content-Type")
		if !strings.Contains(contentType, "text/event-stream") && !strings.Contains(contentType, "text/plain") {
			// 如果不是 SSE 格式，读取前几百字节看看是什么
			peekBody := make([]byte, 500)
			n, _ := resp.Body.Read(peekBody)
			if n > 0 {
				errChan <- fmt.Errorf("unexpected content type: %s, first bytes: %s", contentType, string(peekBody[:n]))
				return
			}
		}

		// 解析 SSE 流
		// SSE 格式：每行以 "data: " 开头，可能包含空行
		reader := bufio.NewReader(resp.Body)
		lineCount := 0
		dataLineCount := 0

		debugPrint("Starting to read SSE stream...")

		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				if err == io.EOF {
					if lineCount == 0 {
						errChan <- fmt.Errorf("no data received from stream")
					}
					break
				}
				errChan <- fmt.Errorf("failed to read stream: %w", err)
				return
			}

			lineCount++
			// 去除换行符
			line = strings.TrimRight(line, "\r\n")

			// 跳过空行
			if line == "" {
				continue
			}

			// 检查是否是 data 行
			if strings.HasPrefix(line, "data: ") {
				dataLineCount++
				data := strings.TrimPrefix(line, "data: ")

				debugPrint("Received data line %d: %s", dataLineCount, data[:min(len(data), 200)])

				// 检查结束标记
				if data == "[DONE]" {
					debugPrint("Received [DONE] marker")
					break
				}

				// 解析 JSON
				var event map[string]interface{}
				if err := json.Unmarshal([]byte(data), &event); err != nil {
					debugPrint("Failed to parse JSON: %v, data: %s", err, data[:min(len(data), 100)])
					// 记录解析错误但继续
					continue
				}

				debugPrint("Parsed event: has choices=%v, has v=%v", event["choices"] != nil, event["v"] != nil)

				// 检查是否是简化格式 {"v":"content"}
				if v, ok := event["v"].(string); ok {
					// 这是简化格式，直接提取内容
					chunk := Chunk{
						Content: v,
						Type:    "text",
					}
					debugPrint("Sending chunk (simplified format): content_len=%d", len(chunk.Content))
					chunkChan <- chunk
					continue
				}

				// 标准格式：解析 choices
				choices, ok := event["choices"].([]interface{})
				if !ok || len(choices) == 0 {
					// 检查是否有 finish_reason 或其他字段
					if finishReason := getString(event, "finish_reason"); finishReason != "" {
						chunk := Chunk{
							FinishReason: finishReason,
						}
						chunkChan <- chunk
						if finishReason == "stop" {
							debugPrint("Received stop signal")
							break
						}
					}
					continue
				}

				choice, ok := choices[0].(map[string]interface{})
				if !ok {
					continue
				}

				// 检查是否有 delta
				delta, ok := choice["delta"].(map[string]interface{})
				if !ok {
					// 可能没有 delta，检查是否有其他字段
					continue
				}

				chunk := Chunk{
					Content:      getString(delta, "content"),
					Type:         getString(delta, "type"),
					FinishReason: getString(choice, "finish_reason"),
				}

				// 尝试从 choice 或 event 中获取 message_id
				if messageID, ok := choice["message_id"].(string); ok {
					chunk.MessageID = messageID
				} else if messageID, ok := event["message_id"].(string); ok {
					chunk.MessageID = messageID
				}

				// 发送 chunk（即使内容为空，也可能有 finish_reason）
				debugPrint("Sending chunk: type=%s, content_len=%d, finish_reason=%s",
					chunk.Type, len(chunk.Content), chunk.FinishReason)
				chunkChan <- chunk

				if chunk.FinishReason == "stop" {
					debugPrint("Received stop signal")
					break
				}
			} else {
				// 记录非 data 行（用于调试）
				// 只在前面几行记录，避免输出太多
				if lineCount <= 5 {
					// 可以在这里添加调试日志
				}
			}
		}

		// 如果读取了行但没有解析到任何数据，报告错误
		debugPrint("Finished reading stream: total_lines=%d, data_lines=%d", lineCount, dataLineCount)
		if lineCount > 0 && dataLineCount == 0 {
			errChan <- fmt.Errorf("received %d lines but no valid data lines found", lineCount)
		} else if lineCount == 0 {
			errChan <- fmt.Errorf("no data received from stream (empty response)")
		}
	}()

	return chunkChan, errChan
}

// getString 从 map 中安全获取字符串值
func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// min 返回两个整数中的较小值
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
