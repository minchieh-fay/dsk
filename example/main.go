package main

import (
	"fmt"
	"os"

	"github.com/minchieh-fay/dsk/dsk"
)

func init() {
	// 可以在这里启用调试模式（如果需要）
	// dsk.EnableDebug = true
}

func main() {
	// 请写入正确的 token
	// 获取方法：用浏览器打开 https://chat.deepseek.com，登录后，在 console 中运行：
	// JSON.parse(localStorage.getItem("userToken")).value
	token := ""

	if token == "" {
		fmt.Fprintf(os.Stderr, "Error: Please set your DeepSeek token in the code\n")
		fmt.Fprintf(os.Stderr, "获取方法：用浏览器打开 https://chat.deepseek.com，登录后，在 console 中运行：\n")
		fmt.Fprintf(os.Stderr, "JSON.parse(localStorage.getItem(\"userToken\")).value\n")
		os.Exit(1)
	}

	// 创建 API 客户端（WASM 文件已嵌入到二进制中）
	api, err := dsk.NewDeepSeekAPI(token)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating API client: %v\n", err)
		os.Exit(1)
	}
	defer api.Close()

	// 创建聊天会话
	fmt.Println("Creating chat session...")
	chatID, err := api.CreateChatSession()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating chat session: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Chat session created: %s\n\n", chatID)

	// 发送消息
	fmt.Println("Sending message: What is Go programming language?")
	fmt.Println("Response:")
	fmt.Println("---")

	fmt.Println("Getting chat completion...")
	chunkChan, errChan := api.ChatCompletion(chatID, "go语言是什么, 请用20字回答", nil, false, false)

	// 处理流式响应
	receivedAny := false

	for {
		select {
		case chunk, ok := <-chunkChan:
			if !ok {
				// channel 已关闭
				if !receivedAny {
					fmt.Fprintf(os.Stderr, "\n⚠️  Warning: No chunks received from stream\n")
					// 等待一下看看是否有错误
					select {
					case err := <-errChan:
						if err != nil {
							fmt.Fprintf(os.Stderr, "❌ Error: %v\n", err)
							os.Exit(1)
						}
					default:
					}
				}
				goto done
			}
			receivedAny = true

			// 打印所有收到的 chunk 信息
			if chunk.Type == "text" {
				fmt.Print(chunk.Content)
			} else if chunk.Type == "thinking" {
				fmt.Printf("\n[Thinking: %s]\n", chunk.Content)
			} else if chunk.Type != "" || chunk.Content != "" {
				fmt.Printf("\n[Type: %s, Content: %s]\n", chunk.Type, chunk.Content)
			}

			if chunk.FinishReason == "stop" {
				goto done
			}
		case err := <-errChan:
			if err != nil {
				fmt.Fprintf(os.Stderr, "\n❌ Error: %v\n", err)
				os.Exit(1)
			}
		}
	}

done:

	if receivedAny {
		fmt.Println("\n---")
		fmt.Println("✅ Stream completed successfully")
	} else {
		fmt.Println("\n---")
		fmt.Println("⚠️  Stream completed but no data received")
	}
}
