package dsk

import (
	"fmt"
	"io"
	"net/http"
	"os"
)

// EnableDebug 启用调试模式，打印详细的请求和响应信息
var EnableDebug = false

func debugPrint(format string, args ...interface{}) {
	if EnableDebug {
		fmt.Fprintf(os.Stderr, "[DEBUG] "+format+"\n", args...)
	}
}

func debugResponse(resp *http.Response) {
	if !EnableDebug {
		return
	}

	debugPrint("Response Status: %d %s", resp.StatusCode, resp.Status)
	debugPrint("Response Headers:")
	for k, v := range resp.Header {
		debugPrint("  %s: %v", k, v)
	}

	// 读取响应体（但不消费它）
	if resp.Body != nil {
		bodyBytes, err := io.ReadAll(resp.Body)
		if err == nil && len(bodyBytes) > 0 {
			bodyStr := string(bodyBytes)
			if len(bodyStr) > 1000 {
				bodyStr = bodyStr[:1000] + "... (truncated)"
			}
			debugPrint("Response Body (first 1000 bytes):\n%s", bodyStr)
			// 注意：这里读取后需要重新设置 Body，但为了简化，我们只在调试时读取
		}
	}
}
