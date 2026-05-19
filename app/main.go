package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

type FunctionDefinition struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

type Tool struct {
	Type     string             `json:"type"`
	Function FunctionDefinition `json:"function"`
}

var tools = []Tool{
	{
		Type: "function",
		Function: FunctionDefinition{
			Name:        "Read",
			Description: "Read and return the contents of a file",
			Parameters: map[string]any{
				"type":     "object",
				"required": []string{"file_path"},
				"properties": map[string]any{
					"file_path": map[string]any{
						"type":        "string",
						"description": "The path to the file to read",
					},
				},
			},
		},
	},
	{
		Type: "function",
		Function: FunctionDefinition{
			Name:        "Write",
			Description: "Write content to a file",
			Parameters: map[string]any{
				"type":     "object",
				"required": []string{"file_path", "content"},
				"properties": map[string]any{
					"file_path": map[string]any{
						"type":        "string",
						"description": "The path of the file to write to",
					},
					"content": map[string]any{
						"type":        "string",
						"description": "The content to write to the file",
					},
				},
			},
		},
	},
	{
		Type: "function",
		Function: FunctionDefinition{
			Name:        "Bash",
			Description: "Execute a shell command",
			Parameters: map[string]any{
				"type":     "object",
				"required": []string{"command"},
				"properties": map[string]any{
					"command": map[string]any{
						"type":        "string",
						"description": "The command to execute",
					},
				},
			},
		},
	},
}

type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	Name       string     `json:"name,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}

type ChatCompletionRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Tools    []Tool    `json:"tools,omitempty"`
}

type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

type ChatCompletionResponse struct {
	Choices []Choice `json:"choices"`
	Error   *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func executeTool(toolCall ToolCall) string {
	name := toolCall.Function.Name
	var args map[string]string
	if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err != nil {
		return fmt.Sprintf("Error parsing tool arguments: %v", err)
	}

	switch name {
	case "Read":
		filePath := args["file_path"]
		content, err := os.ReadFile(filePath)
		if err != nil {
			return fmt.Sprintf("Error: %v", err)
		}
		return string(content)

	case "Write":
		filePath := args["file_path"]
		content := args["content"]
		dir := filepath.Dir(filePath)
		if dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return fmt.Sprintf("Error creating directory: %v", err)
			}
		}
		if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
			return fmt.Sprintf("Error: %v", err)
		}
		return fmt.Sprintf("Successfully wrote to %s", filePath)

	case "Bash":
		command := args["command"]
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		cmd := exec.CommandContext(ctx, "bash", "-c", command)
		output, err := cmd.CombinedOutput()
		if err != nil {
			if len(output) > 0 {
				return string(output)
			}
			return fmt.Sprintf("Command failed with exit code %v", err)
		}
		if len(output) == 0 {
			return "Command executed successfully (no output)"
		}
		return string(output)

	default:
		return fmt.Sprintf("Unknown tool: %s", name)
	}
}

func main() {
	var prompt string
	flag.StringVar(&prompt, "p", "", "Prompt to send to LLM")
	flag.Parse()

	if prompt == "" {
		fmt.Fprintln(os.Stderr, "error: -p flag is required")
		os.Exit(1)
	}

	apiKey := os.Getenv("OPENROUTER_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "OPENROUTER_API_KEY is not set")
		os.Exit(1)
	}

	baseURL := os.Getenv("OPENROUTER_BASE_URL")
	if baseURL == "" {
		baseURL = "https://openrouter.ai/api/v1"
	}

	messages := []Message{
		{
			Role:    "user",
			Content: prompt,
		},
	}

	client := &http.Client{}

	for {
		reqBody := ChatCompletionRequest{
			Model:    "anthropic/claude-haiku-4.5",
			Messages: messages,
			Tools:    tools,
		}

		jsonBytes, err := json.Marshal(reqBody)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error marshaling request: %v\n", err)
			os.Exit(1)
		}

		apiURL := fmt.Sprintf("%s/chat/completions", baseURL)
		req, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(jsonBytes))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating request: %v\n", err)
			os.Exit(1)
		}

		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiKey))
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error sending request: %v\n", err)
			os.Exit(1)
		}
		defer resp.Body.Close()

		respBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading response body: %v\n", err)
			os.Exit(1)
		}

		if resp.StatusCode != http.StatusOK {
			fmt.Fprintf(os.Stderr, "API returned non-OK status %d: %s\n", resp.StatusCode, string(respBytes))
			os.Exit(1)
		}

		var apiResponse ChatCompletionResponse
		if err := json.Unmarshal(respBytes, &apiResponse); err != nil {
			fmt.Fprintf(os.Stderr, "Error unmarshaling response JSON: %v\n", err)
			os.Exit(1)
		}

		if apiResponse.Error != nil {
			fmt.Fprintf(os.Stderr, "API Error: %s\n", apiResponse.Error.Message)
			os.Exit(1)
		}

		if len(apiResponse.Choices) == 0 {
			fmt.Fprintln(os.Stderr, "No choices in API response")
			os.Exit(1)
		}

		assistantMessage := apiResponse.Choices[0].Message

		// Append assistant message to history
		messages = append(messages, assistantMessage)

		if len(assistantMessage.ToolCalls) > 0 {
			for _, toolCall := range assistantMessage.ToolCalls {
				result := executeTool(toolCall)

				messages = append(messages, Message{
					Role:       "tool",
					ToolCallID: toolCall.ID,
					Content:    result,
				})
			}
			continue
		}

		fmt.Fprintln(os.Stderr, "Logs from your program will appear here!")
		fmt.Print(assistantMessage.Content)
		break
	}
}
