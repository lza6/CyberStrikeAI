package security

import (
	"encoding/json"
	"fmt"
	"strings"

	"cyberstrike-ai/internal/config"

	"go.uber.org/zap"
)

// buildCommandArgs 构建命令参数
func (e *Executor) buildCommandArgs(toolName string, toolConfig *config.ToolConfig, args map[string]interface{}) []string {
	cmdArgs := make([]string, 0)

	// 如果配置中定义了参数映射，使用配置中的映射规则
	if len(toolConfig.Parameters) > 0 {
		// 检查是否有 scan_type 参数，如果有则替换默认的扫描类型参数
		hasScanType := false
		var scanTypeValue string
		if scanType, ok := args["scan_type"].(string); ok && scanType != "" {
			hasScanType = true
			scanTypeValue = scanType
		}

		// 添加固定参数（如果指定了 scan_type，可能需要过滤掉默认的扫描类型参数）
		if hasScanType && toolName == "nmap" {
			// 对于 nmap，如果指定了 scan_type，跳过默认的 -sT -sV -sC
			// 这些参数会被 scan_type 参数替换
		} else {
			cmdArgs = append(cmdArgs, toolConfig.Args...)
		}

		// 按位置参数排序
		positionalParams := make([]config.ParameterConfig, 0)
		flagParams := make([]config.ParameterConfig, 0)

		for _, param := range toolConfig.Parameters {
			if param.Position != nil {
				positionalParams = append(positionalParams, param)
			} else {
				flagParams = append(flagParams, param)
			}
		}

		// 对于需要子命令的工具（如 gobuster dir），position 0 必须紧跟在命令名后、所有 flag 之前
		for _, param := range positionalParams {
			if param.Name == "additional_args" || param.Name == "scan_type" || param.Name == "action" {
				continue
			}
			if param.Position != nil && *param.Position == 0 {
				value := e.getParamValue(args, param)
				if value == nil && param.Default != nil {
					value = param.Default
				}
				if value != nil {
					cmdArgs = append(cmdArgs, e.formatParamValue(param, value))
				}
				break
			}
		}

		// 处理标志参数
		for _, param := range flagParams {
			// 跳过特殊参数，它们会在后面单独处理
			// action 参数仅用于工具内部逻辑，不传递给命令
			if param.Name == "additional_args" || param.Name == "scan_type" || param.Name == "action" {
				continue
			}

			value := e.getParamValue(args, param)
			if value == nil {
				if param.Required {
					// 必需参数缺失，返回空数组让上层处理错误
					e.logger.Warn("缺少必需的标志参数",
						zap.String("tool", toolName),
						zap.String("param", param.Name),
					)
					return []string{}
				}
				continue
			}

			// 布尔值特殊处理：如果为 false，跳过；如果为 true，只添加标志
			if param.Type == "bool" {
				var boolVal bool
				var ok bool

				// 尝试多种类型转换
				if boolVal, ok = value.(bool); ok {
					// 已经是布尔值
				} else if numVal, ok := value.(float64); ok {
					// JSON 数字类型（float64）
					boolVal = numVal != 0
					ok = true
				} else if numVal, ok := value.(int); ok {
					// int 类型
					boolVal = numVal != 0
					ok = true
				} else if strVal, ok := value.(string); ok {
					// 字符串类型
					boolVal = strVal == "true" || strVal == "1" || strVal == "yes"
					ok = true
				}

				if ok {
					if !boolVal {
						continue // false 时不添加任何参数
					}
					// true 时只添加标志，不添加值
					if param.Flag != "" {
						cmdArgs = append(cmdArgs, param.Flag)
					}
					continue
				}
			}

			formattedValue := e.formatParamValue(param, value)
			if strings.TrimSpace(formattedValue) == "" {
				if param.Required {
					e.logger.Warn("必需参数为空",
						zap.String("tool", toolName),
						zap.String("param", param.Name),
					)
					return []string{}
				}
				continue
			}

			format := param.Format
			if format == "" {
				format = "flag" // 默认格式
			}

			switch format {
			case "flag":
				// --flag value 或 -f value
				if param.Flag != "" {
					cmdArgs = append(cmdArgs, param.Flag)
				}
				cmdArgs = append(cmdArgs, formattedValue)
			case "combined":
				// --flag=value 或 -f=value
				if param.Flag != "" {
					cmdArgs = append(cmdArgs, fmt.Sprintf("%s=%s", param.Flag, formattedValue))
				} else {
					cmdArgs = append(cmdArgs, formattedValue)
				}
			case "template":
				// 使用模板字符串
				if param.Template != "" {
					template := param.Template
					template = strings.ReplaceAll(template, "{flag}", param.Flag)
					template = strings.ReplaceAll(template, "{value}", formattedValue)
					template = strings.ReplaceAll(template, "{name}", param.Name)
					cmdArgs = append(cmdArgs, strings.Fields(template)...)
				} else {
					// 如果没有模板，使用默认格式
					if param.Flag != "" {
						cmdArgs = append(cmdArgs, param.Flag)
					}
					cmdArgs = append(cmdArgs, formattedValue)
				}
			case "positional":
				// 位置参数（已在上面处理）
				cmdArgs = append(cmdArgs, formattedValue)
			default:
				// 默认：直接添加值
				cmdArgs = append(cmdArgs, formattedValue)
			}
		}

		// 然后处理位置参数（位置参数通常在标志参数之后）
		// 对位置参数按位置排序
		// 首先找到最大的位置值，确定需要处理多少个位置
		maxPosition := -1
		for _, param := range positionalParams {
			if param.Position != nil && *param.Position > maxPosition {
				maxPosition = *param.Position
			}
		}

		// 按位置顺序处理参数，确保即使某些位置没有参数或使用默认值，也能正确传递
		// position 0 已在前面插入（子命令优先），此处从 1 开始
		for i := 0; i <= maxPosition; i++ {
			if i == 0 {
				continue
			}
			for _, param := range positionalParams {
				// 跳过特殊参数，它们会在后面单独处理
				// action 参数仅用于工具内部逻辑，不传递给命令
				if param.Name == "additional_args" || param.Name == "scan_type" || param.Name == "action" {
					continue
				}

				if param.Position != nil && *param.Position == i {
					value := e.getParamValue(args, param)
					if value == nil {
						if param.Required {
							// 必需参数缺失，返回空数组让上层处理错误
							e.logger.Warn("缺少必需的位置参数",
								zap.String("tool", toolName),
								zap.String("param", param.Name),
								zap.Int("position", *param.Position),
							)
							return []string{}
						}
						// 对于非必需参数，如果值为 nil，尝试使用默认值
						if param.Default != nil {
							value = param.Default
						} else {
							// 如果没有默认值，跳过这个位置，继续处理下一个位置
							break
						}
					}
					// 只有当值不为 nil 时才添加到命令参数中
					if value != nil {
						cmdArgs = append(cmdArgs, e.formatParamValue(param, value))
					}
					break
				}
			}
			// 如果某个位置没有找到对应的参数，继续处理下一个位置
			// 这样可以确保位置参数的顺序正确
		}

		// 特殊处理：additional_args 参数（需要按空格分割成多个参数）
		if additionalArgs, ok := args["additional_args"].(string); ok && additionalArgs != "" {
			// 按空格分割，但保留引号内的内容
			additionalArgsList := e.parseAdditionalArgs(additionalArgs)
			cmdArgs = append(cmdArgs, additionalArgsList...)
		}

		// 特殊处理：scan_type 参数（需要按空格分割并插入到合适位置）
		if hasScanType {
			scanTypeArgs := e.parseAdditionalArgs(scanTypeValue)
			if len(scanTypeArgs) > 0 {
				// 对于 nmap，scan_type 应该替换默认的扫描类型参数
				// 由于我们已经跳过了默认的 args，现在需要将 scan_type 插入到合适位置
				// 找到 target 参数的位置（通常是最后一个位置参数）
				insertPos := len(cmdArgs)
				for i := len(cmdArgs) - 1; i >= 0; i-- {
					// target 通常是最后一个非标志参数
					if !strings.HasPrefix(cmdArgs[i], "-") {
						insertPos = i
						break
					}
				}
				// 在 target 之前插入 scan_type 参数
				newArgs := make([]string, 0, len(cmdArgs)+len(scanTypeArgs))
				newArgs = append(newArgs, cmdArgs[:insertPos]...)
				newArgs = append(newArgs, scanTypeArgs...)
				newArgs = append(newArgs, cmdArgs[insertPos:]...)
				cmdArgs = newArgs
			}
		}

		return cmdArgs
	}

	// 如果没有定义参数配置，使用固定参数和通用处理
	// 添加固定参数
	cmdArgs = append(cmdArgs, toolConfig.Args...)

	// 通用处理：将参数转换为命令行参数
	for key, value := range args {
		if key == "_tool_name" {
			continue
		}
		// 使用 --key value 格式
		cmdArgs = append(cmdArgs, fmt.Sprintf("--%s", key))
		if strValue, ok := value.(string); ok {
			cmdArgs = append(cmdArgs, strValue)
		} else {
			cmdArgs = append(cmdArgs, fmt.Sprintf("%v", value))
		}
	}

	return cmdArgs
}

// parseAdditionalArgs 解析 additional_args 字符串，按空格分割但保留引号内的内容
func (e *Executor) parseAdditionalArgs(argsStr string) []string {
	if argsStr == "" {
		return []string{}
	}

	result := make([]string, 0)
	var current strings.Builder
	inQuotes := false
	var quoteChar rune
	escapeNext := false

	runes := []rune(argsStr)
	for i := 0; i < len(runes); i++ {
		r := runes[i]

		if escapeNext {
			current.WriteRune(r)
			escapeNext = false
			continue
		}

		if r == '\\' {
			// 检查下一个字符是否是引号
			if i+1 < len(runes) && (runes[i+1] == '"' || runes[i+1] == '\'') {
				// 转义的引号：跳过反斜杠，将引号作为普通字符写入
				i++
				current.WriteRune(runes[i])
			} else {
				// 其他转义字符：写入反斜杠，下一个字符会在下次迭代处理
				escapeNext = true
				current.WriteRune(r)
			}
			continue
		}

		if !inQuotes && (r == '"' || r == '\'') {
			inQuotes = true
			quoteChar = r
			continue
		}

		if inQuotes && r == quoteChar {
			inQuotes = false
			quoteChar = 0
			continue
		}

		if !inQuotes && (r == ' ' || r == '\t' || r == '\n') {
			if current.Len() > 0 {
				result = append(result, current.String())
				current.Reset()
			}
			continue
		}

		current.WriteRune(r)
	}

	// 处理最后一个参数（如果存在）
	if current.Len() > 0 {
		result = append(result, current.String())
	}

	// 如果解析结果为空，使用简单的空格分割作为降级方案
	if len(result) == 0 {
		result = strings.Fields(argsStr)
	}

	return result
}

// getParamValue 获取参数值，支持默认值
func (e *Executor) getParamValue(args map[string]interface{}, param config.ParameterConfig) interface{} {
	// 从参数中获取值
	if value, ok := args[param.Name]; ok && value != nil {
		return value
	}

	// 如果参数是必需的但没有提供，返回 nil（让上层处理错误）
	if param.Required {
		return nil
	}

	// 返回默认值
	return param.Default
}

// formatParamValue 格式化参数值
func (e *Executor) formatParamValue(param config.ParameterConfig, value interface{}) string {
	switch param.Type {
	case "bool":
		// 布尔值应该在上层处理，这里不应该被调用
		if boolVal, ok := value.(bool); ok {
			return fmt.Sprintf("%v", boolVal)
		}
		return "false"
	case "array":
		// 数组：转换为逗号分隔的字符串
		if arr, ok := value.([]interface{}); ok {
			strs := make([]string, 0, len(arr))
			for _, item := range arr {
				strs = append(strs, fmt.Sprintf("%v", item))
			}
			return strings.Join(strs, ",")
		}
		return fmt.Sprintf("%v", value)
	case "object":
		// 对象/字典：序列化为 JSON 字符串
		if jsonBytes, err := json.Marshal(value); err == nil {
			return string(jsonBytes)
		}
		// 如果 JSON 序列化失败，回退到默认格式化
		return fmt.Sprintf("%v", value)
	default:
		formattedValue := fmt.Sprintf("%v", value)
		// 特殊处理：对于 ports 参数（通常是 nmap 等工具的端口参数），清理空格
		// nmap 不接受端口列表中有空格，例如 "80,443, 22" 应该变成 "80,443,22"
		if param.Name == "ports" {
			// 移除所有空格，但保留逗号和其他字符
			formattedValue = strings.ReplaceAll(formattedValue, " ", "")
		}
		return formattedValue
	}
}

// IsBackgroundShellCommand 检测命令是否为完全后台命令（末尾有独立 &，且不在引号内）。// buildInputSchema 构建输入模式
func (e *Executor) buildInputSchema(toolConfig *config.ToolConfig) map[string]interface{} {
	schema := map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
		"required":   []string{},
	}

	// 如果配置中定义了参数，优先使用配置中的参数定义
	if len(toolConfig.Parameters) > 0 {
		properties := make(map[string]interface{})
		required := []string{}

		for _, param := range toolConfig.Parameters {
			// 跳过 name 为空的参数（避免 YAML 中 name: null 或空导致非法 schema）
			if strings.TrimSpace(param.Name) == "" {
				e.logger.Debug("跳过无名称的参数",
					zap.String("tool", toolConfig.Name),
					zap.String("type", param.Type),
				)
				continue
			}
			// 转换类型为OpenAI/JSON Schema标准类型（空类型默认为 string）
			openAIType := e.convertToOpenAIType(param.Type)

			prop := map[string]interface{}{
				"type":        openAIType,
				"description": param.Description,
			}

			// JSON Schema/OpenAI 要求 array 类型必须包含 items，否则 API 报 invalid_function_parameters
			if openAIType == "array" {
				itemType := strings.TrimSpace(param.ItemType)
				if itemType == "" {
					itemType = "string"
				}
				prop["items"] = map[string]interface{}{
					"type": e.convertToOpenAIType(itemType),
				}
			}

			// 添加默认值
			if param.Default != nil {
				prop["default"] = param.Default
			}

			// 添加枚举选项
			if len(param.Options) > 0 {
				prop["enum"] = param.Options
			}

			properties[param.Name] = prop

			// 添加到必需参数列表
			if param.Required {
				required = append(required, param.Name)
			}
		}

		schema["properties"] = properties
		schema["required"] = required
		return schema
	}

	// 如果没有定义参数配置，返回空schema
	// 这种情况下工具可能只使用固定参数（args字段）
	// 或者需要通过YAML配置文件定义参数
	e.logger.Warn("工具未定义参数配置，返回空schema",
		zap.String("tool", toolConfig.Name),
	)
	return schema
}

// convertToOpenAIType 将配置中的类型转换为OpenAI/JSON Schema标准类型
func (e *Executor) convertToOpenAIType(configType string) string {
	// 空或 null 类型统一视为 string，避免非法 schema 导致工具调用失败
	if strings.TrimSpace(configType) == "" {
		return "string"
	}
	switch configType {
	case "bool":
		return "boolean"
	case "int", "integer":
		return "number"
	case "float", "double":
		return "number"
	case "string", "array", "object":
		return configType
	default:
		// 默认返回原类型，但记录警告
		e.logger.Warn("未知的参数类型，使用原类型",
			zap.String("type", configType),
		)
		return configType
	}
}
