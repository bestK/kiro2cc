package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// TokenData 表示token文件的结构
type TokenData struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresAt    string `json:"expiresAt,omitempty"`
}

// RefreshRequest 刷新token的请求结构
type RefreshRequest struct {
	RefreshToken string `json:"refreshToken"`
}

// RefreshResponse 刷新token的响应结构
type RefreshResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresAt    string `json:"expiresAt,omitempty"`
}

// AnthropicRequest 表示 Anthropic API 的请求结构
type AnthropicRequest struct {
	Model     string                    `json:"model"`
	MaxTokens int                       `json:"max_tokens"`
	Messages  []AnthropicRequestMessage `json:"messages"`
	Stream    bool                      `json:"stream"`
}

// AnthropicStreamResponse 表示 Anthropic 流式响应的结构
type AnthropicStreamResponse struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentDelta struct {
		Text string `json:"text"`
		Type string `json:"type"`
	} `json:"delta,omitempty"`
	Content []struct {
		Text string `json:"text"`
		Type string `json:"type"`
	} `json:"content,omitempty"`
	StopReason   string `json:"stop_reason,omitempty"`
	StopSequence string `json:"stop_sequence,omitempty"`
	Usage        struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage,omitempty"`
}

// AnthropicRequestMessage 表示 Anthropic API 的消息结构
type AnthropicRequestMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // 可以是 string 或 []ContentBlock
}

// ContentBlock 表示消息内容块的结构
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// getMessageContent 从消息中提取文本内容
func getMessageContent(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var texts []string
		for _, block := range v {
			if blockMap, ok := block.(map[string]any); ok {
				if text, ok := blockMap["text"].(string); ok {
					texts = append(texts, text)
				}
			}
		}
		return strings.Join(texts, "\n")
	default:
		return ""
	}
}

// CodeWhispererRequest 表示 CodeWhisperer API 的请求结构
type CodeWhispererRequest struct {
	ConversationState struct {
		ChatTriggerType string `json:"chatTriggerType"`
		ConversationId  string `json:"conversationId"`
		CurrentMessage  struct {
			UserInputMessage struct {
				Content                 string         `json:"content"`
				ModelId                 string         `json:"modelId"`
				Origin                  string         `json:"origin"`
				UserInputMessageContext map[string]any `json:"userInputMessageContext"`
			} `json:"userInputMessage"`
		} `json:"currentMessage"`
		History []struct {
			UserInputMessage struct {
				Content string `json:"content"`
				ModelId string `json:"modelId"`
				Origin  string `json:"origin"`
			} `json:"userInputMessage,omitempty"`
			AssistantResponseMessage struct {
				Content  string `json:"content"`
				ToolUses []any  `json:"toolUses"`
			} `json:"assistantResponseMessage,omitempty"`
		} `json:"history"`
	} `json:"conversationState"`
	ProfileArn string `json:"profileArn"`
}

// CodeWhispererEvent 表示 CodeWhisperer 的事件响应
type CodeWhispererEvent struct {
	ContentType string `json:"content-type"`
	MessageType string `json:"message-type"`
	Content     string `json:"content"`
	EventType   string `json:"event-type"`
}

var ModelMap = map[string]string{
	"claude-sonnet-4-20250514":  "CLAUDE_SONNET_4_20250514_V1_0",
	"claude-3-5-haiku-20241022": "CLAUDE_3_7_SONNET_20250219_V1_0",
}

// generateUUID generates a simple UUID v4
func generateUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40 // Version 4
	b[8] = (b[8] & 0x3f) | 0x80 // Variant bits
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// ProxyConfig 代理配置结构
type ProxyConfig struct {
	URL       string
	Available bool
	Direct    bool
}

// detectProxyConfig 检测并配置代理设置
func detectProxyConfig() ProxyConfig {
	config := ProxyConfig{Direct: true}

	// 检查环境变量
	proxyEnvVars := []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy"}
	for _, envVar := range proxyEnvVars {
		if proxyURL := os.Getenv(envVar); proxyURL != "" {
			config.URL = proxyURL
			config.Direct = false
			config.Available = testProxyConnection(proxyURL)
			fmt.Printf("检测到代理配置: %s=%s, 可用性: %v\n", envVar, proxyURL, config.Available)
			break
		}
	}

	// 如果没有环境变量，检查传统的硬编码代理（向后兼容）
	if config.Direct {
		legacyProxy := "http://127.0.0.1:9000"
		if testProxyConnection(legacyProxy) {
			fmt.Printf("检测到本地代理服务: %s\n", legacyProxy)
			config.URL = legacyProxy
			config.Direct = false
			config.Available = true
		} else {
			fmt.Printf("未检测到代理配置，使用直连模式\n")
		}
	}

	return config
}

// testProxyConnection 测试代理连接是否可用
func testProxyConnection(proxyURL string) bool {
	parsedURL, err := url.Parse(proxyURL)
	if err != nil {
		fmt.Printf("代理URL解析失败: %v\n", err)
		return false
	}

	// 尝试连接代理服务器
	conn, err := net.DialTimeout("tcp", parsedURL.Host, 2*time.Second)
	if err != nil {
		fmt.Printf("代理连接测试失败: %v\n", err)
		return false
	}
	conn.Close()
	return true
}

// createHTTPClient 根据配置创建HTTP客户端
func createHTTPClient() (*http.Client, error) {
	config := detectProxyConfig()

	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
	}

	// 如果配置了代理且可用，使用代理
	if !config.Direct && config.Available {
		proxyURL, err := url.Parse(config.URL)
		if err != nil {
			return nil, fmt.Errorf("代理URL解析失败: %v", err)
		}
		transport.Proxy = http.ProxyURL(proxyURL)
		fmt.Printf("使用代理模式: %s\n", config.URL)
	} else if !config.Direct && !config.Available {
		// 代理配置了但不可用，打印警告并降级到直连
		fmt.Printf("警告: 配置的代理 %s 不可用，降级使用直连模式\n", config.URL)
	}

	return &http.Client{
		Transport: transport,
		Timeout:   60 * time.Second,
	}, nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("用法:")
		fmt.Println("  kiro2cc read    - 读取并显示token")
		fmt.Println("  kiro2cc refresh - 刷新token")
		fmt.Println("  kiro2cc export  - 导出环境变量")
		fmt.Println("  kiro2cc server [port] - 启动Anthropic API代理服务器")
		os.Exit(1)
	}

	command := os.Args[1]

	switch command {
	case "read":
		readToken()
	case "refresh":
		refreshToken()
	case "export":
		exportEnvVars()
	case "server":
		port := "8080" // 默认端口
		if len(os.Args) > 2 {
			port = os.Args[2]
		}
		startServer(port)
	default:
		fmt.Printf("未知命令: %s\n", command)
		os.Exit(1)
	}
}

// getTokenFilePath 获取跨平台的token文件路径
func getTokenFilePath() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Printf("获取用户目录失败: %v\n", err)
		os.Exit(1)
	}

	return filepath.Join(homeDir, ".aws", "sso", "cache", "kiro-auth-token.json")
}

// readToken 读取并显示token信息
func readToken() {
	tokenPath := getTokenFilePath()

	data, err := os.ReadFile(tokenPath)
	if err != nil {
		fmt.Printf("读取token文件失败: %v\n", err)
		os.Exit(1)
	}

	var token TokenData
	if err := json.Unmarshal(data, &token); err != nil {
		fmt.Printf("解析token文件失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Token信息:")
	fmt.Printf("Access Token: %s\n", token.AccessToken)
	fmt.Printf("Refresh Token: %s\n", token.RefreshToken)
	if token.ExpiresAt != "" {
		fmt.Printf("过期时间: %s\n", token.ExpiresAt)
	}
}

// refreshToken 刷新token
func refreshToken() {
	tokenPath := getTokenFilePath()

	// 读取当前token
	data, err := os.ReadFile(tokenPath)
	if err != nil {
		fmt.Printf("读取token文件失败: %v\n", err)
		os.Exit(1)
	}

	var currentToken TokenData
	if err := json.Unmarshal(data, &currentToken); err != nil {
		fmt.Printf("解析token文件失败: %v\n", err)
		os.Exit(1)
	}

	// 准备刷新请求
	refreshReq := RefreshRequest{
		RefreshToken: currentToken.RefreshToken,
	}

	reqBody, err := json.Marshal(refreshReq)
	if err != nil {
		fmt.Printf("序列化请求失败: %v\n", err)
		os.Exit(1)
	}

	// 发送刷新请求
	resp, err := http.Post(
		"https://prod.us-east-1.auth.desktop.kiro.dev/refreshToken",
		"application/json",
		bytes.NewBuffer(reqBody),
	)
	if err != nil {
		fmt.Printf("刷新token请求失败: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		fmt.Printf("刷新token失败，状态码: %d, 响应: %s\n", resp.StatusCode, string(body))
		os.Exit(1)
	}

	// 解析响应
	var refreshResp RefreshResponse
	if err := json.NewDecoder(resp.Body).Decode(&refreshResp); err != nil {
		fmt.Printf("解析刷新响应失败: %v\n", err)
		os.Exit(1)
	}

	// 更新token文件
	newToken := TokenData{
		AccessToken:  refreshResp.AccessToken,
		RefreshToken: refreshResp.RefreshToken,
		ExpiresAt:    refreshResp.ExpiresAt,
	}

	newData, err := json.MarshalIndent(newToken, "", "  ")
	if err != nil {
		fmt.Printf("序列化新token失败: %v\n", err)
		os.Exit(1)
	}

	if err := os.WriteFile(tokenPath, newData, 0600); err != nil {
		fmt.Printf("写入token文件失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Token刷新成功!")
	fmt.Printf("新的Access Token: %s\n", newToken.AccessToken)
}

// exportEnvVars 导出环境变量
func exportEnvVars() {
	tokenPath := getTokenFilePath()

	data, err := os.ReadFile(tokenPath)
	if err != nil {
		fmt.Printf("读取token文件失败: %v\n", err)
		os.Exit(1)
	}

	var token TokenData
	if err := json.Unmarshal(data, &token); err != nil {
		fmt.Printf("解析token文件失败: %v\n", err)
		os.Exit(1)
	}

	// 根据操作系统输出不同格式的环境变量设置命令
	if runtime.GOOS == "windows" {
		fmt.Printf("set ANTHROPIC_BASE_URL=https://localhost:8080")
		fmt.Printf("set ANTHROPIC_API_KEY=%s\n", token.AccessToken)
	} else {
		fmt.Printf("export ANTHROPIC_BASE_URL=https://localhost:8080")
		fmt.Printf("export ANTHROPIC_API_KEY=\"%s\"\n", token.AccessToken)
	}
}

// getToken 获取当前token
func getToken() (TokenData, error) {
	tokenPath := getTokenFilePath()

	data, err := os.ReadFile(tokenPath)
	if err != nil {
		return TokenData{}, fmt.Errorf("读取token文件失败: %v", err)
	}

	var token TokenData
	if err := json.Unmarshal(data, &token); err != nil {
		return TokenData{}, fmt.Errorf("解析token文件失败: %v", err)
	}

	return token, nil
}

// logMiddleware 记录所有HTTP请求的中间件
func logMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		startTime := time.Now()

		fmt.Printf("\n=== 收到请求 ===\n")
		fmt.Printf("时间: %s\n", startTime.Format("2006-01-02 15:04:05"))
		fmt.Printf("请求方法: %s\n", r.Method)
		fmt.Printf("请求路径: %s\n", r.URL.Path)
		fmt.Printf("客户端IP: %s\n", r.RemoteAddr)
		fmt.Printf("请求头:\n")
		for name, values := range r.Header {
			fmt.Printf("  %s: %s\n", name, strings.Join(values, ", "))
		}

		// 调用下一个处理器
		next(w, r)

		// 计算处理时间
		duration := time.Since(startTime)
		fmt.Printf("处理时间: %v\n", duration)
		fmt.Printf("=== 请求结束 ===\n\n")
	}
}

// startServer 启动HTTP代理服务器
func startServer(port string) {
	// 创建路由器
	mux := http.NewServeMux()

	// 注册所有端点
	mux.HandleFunc("/v1/messages", logMiddleware(func(w http.ResponseWriter, r *http.Request) {
		// 只处理POST请求
		if r.Method != http.MethodPost {
			fmt.Printf("错误: 不支持的请求方法\n")
			http.Error(w, "只支持POST请求", http.StatusMethodNotAllowed)
			return
		}

		// 获取当前token
		token, err := getToken()
		if err != nil {
			fmt.Printf("错误: 获取token失败: %v\n", err)
			http.Error(w, fmt.Sprintf("获取token失败: %v", err), http.StatusInternalServerError)
			return
		}

		// 读取请求体
		body, err := io.ReadAll(r.Body)
		if err != nil {
			fmt.Printf("错误: 读取请求体失败: %v\n", err)
			http.Error(w, fmt.Sprintf("读取请求体失败: %v", err), http.StatusInternalServerError)
			return
		}
		defer r.Body.Close()

		fmt.Printf("Anthropic 请求体:\n%s\n", string(body))

		// 解析 Anthropic 请求
		var anthropicReq AnthropicRequest
		if err := json.Unmarshal(body, &anthropicReq); err != nil {
			fmt.Printf("错误: 解析请求体失败: %v\n", err)
			http.Error(w, fmt.Sprintf("解析请求体失败: %v", err), http.StatusBadRequest)
			return
		}

		// 如果是流式请求
		if anthropicReq.Stream {
			handleStreamRequest(w, anthropicReq, token.AccessToken)
			return
		}

		// 非流式请求处理
		handleNonStreamRequest(w, anthropicReq, token.AccessToken)
	}))

	// 添加健康检查端点
	mux.HandleFunc("/health", logMiddleware(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))

	// 添加404处理
	mux.HandleFunc("/", logMiddleware(func(w http.ResponseWriter, r *http.Request) {
		fmt.Printf("警告: 访问未知端点\n")
		http.Error(w, "404 未找到", http.StatusNotFound)
	}))

	// 启动服务器
	fmt.Printf("启动Anthropic API代理服务器，监听端口: %s\n", port)
	fmt.Printf("可用端点:\n")
	fmt.Printf("  POST /v1/messages - Anthropic API代理\n")
	fmt.Printf("  GET  /health      - 健康检查\n")
	fmt.Printf("按Ctrl+C停止服务器\n")

	if err := http.ListenAndServe(":"+port, mux); err != nil {
		fmt.Printf("启动服务器失败: %v\n", err)
		os.Exit(1)
	}
}

// handleStreamRequest 处理流式请求
func handleStreamRequest(w http.ResponseWriter, anthropicReq AnthropicRequest, accessToken string) {
	// 设置SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported!", http.StatusInternalServerError)
		return
	}

	messageId := fmt.Sprintf("msg_%s", time.Now().Format("20060102150405"))

	// 发送开始事件
	messageStart := map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            messageId,
			"type":          "message",
			"role":          "assistant",
			"content":       []any{},
			"model":         anthropicReq.Model,
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage": map[string]any{
				"input_tokens":  0,
				"output_tokens": 0,
			},
		},
	}
	sendSSEEvent(w, flusher, "message_start", messageStart)

	contentBlockStart := map[string]any{
		"type":  "content_block_start",
		"index": 0,
		"content_block": map[string]any{
			"type": "text",
			"text": "",
		},
	}
	sendSSEEvent(w, flusher, "content_block_start", contentBlockStart)

	// 构建 CodeWhisperer 请求
	cwReq := buildCodeWhispererRequest(anthropicReq)

	// 序列化请求体
	cwReqBody, err := json.Marshal(cwReq)
	if err != nil {
		sendErrorEvent(w, flusher, "序列化请求失败", err)
		return
	}

	fmt.Printf("CodeWhisperer 流式请求体:\n%s\n", string(cwReqBody))

	// 创建流式请求
	proxyReq, err := http.NewRequest(
		http.MethodPost,
		"https://codewhisperer.us-east-1.amazonaws.com/generateAssistantResponse",
		bytes.NewBuffer(cwReqBody),
	)
	if err != nil {
		sendErrorEvent(w, flusher, "创建代理请求失败", err)
		return
	}

	// 设置请求头
	proxyReq.Header.Set("Authorization", "Bearer "+accessToken)
	proxyReq.Header.Set("Content-Type", "application/json")
	proxyReq.Header.Set("Accept", "text/event-stream")

	// 创建HTTP客户端
	client, err := createHTTPClient()
	if err != nil {
		sendErrorEvent(w, flusher, "创建HTTP客户端失败", err)
		return
	}

	// 发送请求
	resp, err := client.Do(proxyReq)
	if err != nil {
		// 提供更详细的错误信息和诊断建议
		errorMsg := fmt.Sprintf("发送流式请求失败: %v", err)
		if strings.Contains(err.Error(), "connection refused") {
			errorMsg += "\n建议检查:\n1. 网络连接是否正常\n2. 目标服务器是否可访问\n3. 如使用代理，请确认代理服务正在运行\n4. 可设置 HTTP_PROXY 环境变量或使用直连模式"
		}
		sendErrorEvent(w, flusher, errorMsg, nil)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		fmt.Printf("CodeWhisperer 响应错误，状态码: %d, 响应: %s\n", resp.StatusCode, string(body))
		sendErrorEvent(w, flusher, "CodeWhisperer API 错误", fmt.Errorf("状态码: %d", resp.StatusCode))
		return
	}

	// 先读取整个响应体
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		sendErrorEvent(w, flusher, "读取响应体失败", err)
		return
	}

	// 提取所有JSON内容并合并
	var allContent strings.Builder
	respStr := string(respBody)

	// 使用正则表达式或字符串搜索提取所有JSON内容
	for {
		jsonStart := strings.Index(respStr, `{"content":"`)
		if jsonStart == -1 {
			break
		}

		// 从JSON开始位置查找
		remaining := respStr[jsonStart:]
		jsonEnd := strings.Index(remaining, `"}`)
		if jsonEnd == -1 {
			break
		}

		jsonStr := remaining[:jsonEnd+2] // 包含结束的 "}

		// 解析JSON
		var cwEvent map[string]any
		if err := json.Unmarshal([]byte(jsonStr), &cwEvent); err != nil {
			fmt.Printf("解析JSON失败: %v\n", err)
			respStr = remaining[jsonEnd+2:]
			continue
		}

		// 提取内容
		if content, ok := cwEvent["content"].(string); ok && content != "" {
			allContent.WriteString(content)
		}

		// 继续搜索下一个JSON
		respStr = remaining[jsonEnd+2:]
	}

	// 获取完整内容
	fullContent := allContent.String()
	fmt.Printf("提取的完整内容: %s\n", fullContent)

	if fullContent == "" {
		sendErrorEvent(w, flusher, "未找到有效内容", fmt.Errorf("响应中没有content字段"))
		return
	}

	// 将完整内容转换为流式输出
	// 按单词分割，模拟流式输出
	words := strings.Fields(fullContent)
	var outputTokens int

	for i, word := range words {
		// 添加空格（除了第一个单词）
		content := word
		if i > 0 {
			content = " " + word
		}

		// 发送 content_block_delta 事件
		contentDelta := map[string]any{
			"type":  "content_block_delta",
			"index": 0,
			"delta": map[string]any{
				"type": "text_delta",
				"text": content,
			},
		}
		sendSSEEvent(w, flusher, "content_block_delta", contentDelta)
		outputTokens++

		// 添加小延迟模拟流式效果
		time.Sleep(50 * time.Millisecond)
	}

	// 发送结束事件
	contentBlockStop := map[string]any{
		"type":  "content_block_stop",
		"index": 0,
	}
	sendSSEEvent(w, flusher, "content_block_stop", contentBlockStop)

	messageDelta := map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   "end_turn",
			"stop_sequence": nil,
		},
		"usage": map[string]any{
			"output_tokens": outputTokens,
		},
	}
	sendSSEEvent(w, flusher, "message_delta", messageDelta)

	messageStop := map[string]any{
		"type": "message_stop",
	}
	sendSSEEvent(w, flusher, "message_stop", messageStop)
}

// handleNonStreamRequest 处理非流式请求
func handleNonStreamRequest(w http.ResponseWriter, anthropicReq AnthropicRequest, accessToken string) {
	// 构建 CodeWhisperer 请求
	cwReq := buildCodeWhispererRequest(anthropicReq)

	// 序列化请求体
	cwReqBody, err := json.Marshal(cwReq)
	if err != nil {
		fmt.Printf("错误: 序列化请求失败: %v\n", err)
		http.Error(w, fmt.Sprintf("序列化请求失败: %v", err), http.StatusInternalServerError)
		return
	}

	fmt.Printf("CodeWhisperer 请求体:\n%s\n", string(cwReqBody))

	// 创建请求
	proxyReq, err := http.NewRequest(
		http.MethodPost,
		"https://codewhisperer.us-east-1.amazonaws.com/generateAssistantResponse",
		bytes.NewBuffer(cwReqBody),
	)
	if err != nil {
		fmt.Printf("错误: 创建代理请求失败: %v\n", err)
		http.Error(w, fmt.Sprintf("创建代理请求失败: %v", err), http.StatusInternalServerError)
		return
	}

	// 设置请求头
	proxyReq.Header.Set("Authorization", "Bearer "+accessToken)
	proxyReq.Header.Set("Content-Type", "application/json")

	// 创建HTTP客户端
	client, err := createHTTPClient()
	if err != nil {
		fmt.Printf("错误: 创建HTTP客户端失败: %v\n", err)
		http.Error(w, fmt.Sprintf("创建HTTP客户端失败: %v", err), http.StatusInternalServerError)
		return
	}

	// 发送请求
	resp, err := client.Do(proxyReq)
	if err != nil {
		// 提供更详细的错误信息和诊断建议
		errorMsg := fmt.Sprintf("发送请求失败: %v", err)
		if strings.Contains(err.Error(), "connection refused") {
			errorMsg += "\n建议检查:\n1. 网络连接是否正常\n2. 目标服务器是否可访问\n3. 如使用代理，请确认代理服务正在运行\n4. 可设置 HTTP_PROXY 环境变量或使用直连模式"
		}
		fmt.Printf("错误: %s\n", errorMsg)
		http.Error(w, errorMsg, http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	// 读取响应
	cwRespBody, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Printf("错误: 读取响应失败: %v\n", err)
		http.Error(w, fmt.Sprintf("读取响应失败: %v", err), http.StatusInternalServerError)
		return
	}

	fmt.Printf("CodeWhisperer 响应体:\n%s\n", string(cwRespBody))

	// 构建 Anthropic 响应
	anthropicResp := map[string]any{
		"content": []map[string]any{
			{
				"text": string(cwRespBody),
				"type": "text",
			},
		},
		"model":         anthropicReq.Model,
		"role":          "assistant",
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"type":          "message",
		"usage": map[string]any{
			"input_tokens":  0,
			"output_tokens": 0,
		},
	}

	// 发送响应
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(anthropicResp)
}

// buildCodeWhispererRequest 构建 CodeWhisperer 请求
func buildCodeWhispererRequest(anthropicReq AnthropicRequest) CodeWhispererRequest {
	cwReq := CodeWhispererRequest{
		ProfileArn: "arn:aws:codewhisperer:us-east-1:699475941385:profile/EHGA3GRVQMUK",
	}
	cwReq.ConversationState.ChatTriggerType = "MANUAL"
	cwReq.ConversationState.ConversationId = generateUUID()
	cwReq.ConversationState.CurrentMessage.UserInputMessage.Content = getMessageContent(anthropicReq.Messages[len(anthropicReq.Messages)-1].Content)
	cwReq.ConversationState.CurrentMessage.UserInputMessage.ModelId = ModelMap[anthropicReq.Model]
	cwReq.ConversationState.CurrentMessage.UserInputMessage.Origin = "AI_EDITOR"
	cwReq.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext = make(map[string]any)

	// 构建历史消息
	if len(anthropicReq.Messages) > 1 {
		history := make([]struct {
			UserInputMessage struct {
				Content string `json:"content"`
				ModelId string `json:"modelId"`
				Origin  string `json:"origin"`
			} `json:"userInputMessage,omitempty"`
			AssistantResponseMessage struct {
				Content  string `json:"content"`
				ToolUses []any  `json:"toolUses"`
			} `json:"assistantResponseMessage,omitempty"`
		}, 0)

		for i := 0; i < len(anthropicReq.Messages)-1; i += 2 {
			var historyItem struct {
				UserInputMessage struct {
					Content string `json:"content"`
					ModelId string `json:"modelId"`
					Origin  string `json:"origin"`
				} `json:"userInputMessage,omitempty"`
				AssistantResponseMessage struct {
					Content  string `json:"content"`
					ToolUses []any  `json:"toolUses"`
				} `json:"assistantResponseMessage,omitempty"`
			}

			if anthropicReq.Messages[i].Role == "user" {
				historyItem.UserInputMessage.Content = getMessageContent(anthropicReq.Messages[i].Content)
				historyItem.UserInputMessage.ModelId = ModelMap[anthropicReq.Model]
				historyItem.UserInputMessage.Origin = "AI_EDITOR"

				if i+1 < len(anthropicReq.Messages)-1 && anthropicReq.Messages[i+1].Role == "assistant" {
					historyItem.AssistantResponseMessage.Content = getMessageContent(anthropicReq.Messages[i+1].Content)
					historyItem.AssistantResponseMessage.ToolUses = make([]any, 0)
				}

				history = append(history, historyItem)
			}
		}

		cwReq.ConversationState.History = history
	}

	return cwReq
}

// sendSSEEvent 发送 SSE 事件
func sendSSEEvent(w http.ResponseWriter, flusher http.Flusher, eventType string, data any) {
	dataBytes, err := json.Marshal(data)
	if err != nil {
		fmt.Printf("错误: 序列化事件数据失败: %v\n", err)
		return
	}

	fmt.Fprintf(w, "event: %s\n", eventType)
	fmt.Fprintf(w, "data: %s\n\n", string(dataBytes))
	flusher.Flush()
}

// sendErrorEvent 发送错误事件
func sendErrorEvent(w http.ResponseWriter, flusher http.Flusher, message string, err error) {
	var finalMessage string
	if err != nil {
		finalMessage = fmt.Sprintf("%s: %v", message, err)
	} else {
		finalMessage = message
	}

	errorResp := map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    "server_error",
			"message": finalMessage,
		},
	}
	sendSSEEvent(w, flusher, "error", errorResp)
}
