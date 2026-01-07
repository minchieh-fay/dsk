package dsk

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

//go:embed wasm/sha3_wasm_bg.7b9ca65ddd.wasm
var embeddedWASM []byte

// DeepSeekHash 处理哈希计算
type DeepSeekHash struct {
	ctx      context.Context
	runtime  wazero.Runtime
	module   wazero.CompiledModule
	instance api.Module
	memory   api.Memory
}

// DeepSeekPOW 处理 DeepSeek 的 Proof of Work 挑战
type DeepSeekPOW struct {
	hasher *DeepSeekHash
}

// ChallengeConfig 表示 PoW 挑战的配置
type ChallengeConfig struct {
	Algorithm  string `json:"algorithm"`
	Challenge  string `json:"challenge"`
	Salt       string `json:"salt"`
	Difficulty int    `json:"difficulty"`
	ExpireAt   int    `json:"expire_at"`
	Signature  string `json:"signature"`
	TargetPath string `json:"target_path"`
}

// NewDeepSeekPOW 创建一个新的 PoW 求解器
// 如果 wasmPath 为空，使用嵌入的 WASM 文件；否则从文件系统读取
func NewDeepSeekPOW(wasmPath string) (*DeepSeekPOW, error) {
	var wasmBytes []byte
	var err error

	if wasmPath == "" {
		// 使用嵌入的 WASM 文件
		wasmBytes = embeddedWASM
		if len(wasmBytes) == 0 {
			return nil, fmt.Errorf("embedded WASM file is empty")
		}
		debugPrint("Using embedded WASM file (size: %d bytes)", len(wasmBytes))
	} else {
		// 从文件系统读取
		wasmBytes, err = os.ReadFile(wasmPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read WASM file: %w", err)
		}
		debugPrint("Using WASM file from disk: %s (size: %d bytes)", wasmPath, len(wasmBytes))
	}

	hasher, err := newDeepSeekHashFromBytes(wasmBytes)
	if err != nil {
		return nil, err
	}

	return &DeepSeekPOW{
		hasher: hasher,
	}, nil
}

// newDeepSeekHashFromBytes 从字节数据创建哈希计算器
func newDeepSeekHashFromBytes(wasmBytes []byte) (*DeepSeekHash, error) {
	ctx := context.Background()
	hash := &DeepSeekHash{
		ctx: ctx,
	}

	// 创建运行时
	hash.runtime = wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig())

	// 设置 WASI
	_, err := wasi_snapshot_preview1.Instantiate(ctx, hash.runtime)
	if err != nil {
		return nil, fmt.Errorf("failed to instantiate WASI: %w", err)
	}

	// 编译 WASM 模块
	hash.module, err = hash.runtime.CompileModule(ctx, wasmBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to compile WASM module: %w", err)
	}

	// 实例化模块
	hash.instance, err = hash.runtime.InstantiateModule(ctx, hash.module, wazero.NewModuleConfig())
	if err != nil {
		return nil, fmt.Errorf("failed to instantiate WASM module: %w", err)
	}

	// 获取内存
	hash.memory = hash.instance.ExportedMemory("memory")
	if hash.memory == nil {
		return nil, fmt.Errorf("memory not found in WASM module")
	}

	return hash, nil
}

// newDeepSeekHash 从文件路径创建哈希计算器（已废弃，保留用于兼容性）
// Deprecated: Use newDeepSeekHashFromBytes instead
func newDeepSeekHash(wasmPath string) (*DeepSeekHash, error) {
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read WASM file: %w", err)
	}
	return newDeepSeekHashFromBytes(wasmBytes)
}

// writeToMemory 将字符串写入 WASM 内存
func (h *DeepSeekHash) writeToMemory(text string) (uint32, uint32, error) {
	encoded := []byte(text)
	length := uint32(len(encoded))

	// 调用 __wbindgen_export_0 分配内存
	wbindgenExport0 := h.instance.ExportedFunction("__wbindgen_export_0")
	if wbindgenExport0 == nil {
		return 0, 0, fmt.Errorf("__wbindgen_export_0 function not found")
	}

	result, err := wbindgenExport0.Call(h.ctx, uint64(length), 1)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to allocate memory: %w", err)
	}

	ptr := uint32(result[0])

	// 写入内存
	if !h.memory.Write(ptr, encoded) {
		return 0, 0, fmt.Errorf("failed to write to memory")
	}

	return ptr, length, nil
}

// calculateHash 计算哈希值
func (h *DeepSeekHash) calculateHash(algorithm, challenge, salt string, difficulty int, expireAt int) (int, error) {
	prefix := fmt.Sprintf("%s_%d_", salt, expireAt)

	// 获取栈指针函数
	stackPointer := h.instance.ExportedFunction("__wbindgen_add_to_stack_pointer")
	if stackPointer == nil {
		return 0, fmt.Errorf("__wbindgen_add_to_stack_pointer function not found")
	}

	// 分配返回值的空间（-16 字节）
	// 这个函数返回新的栈指针值，也就是 retptr
	// 注意：-16 需要转换为 uint64，使用补码表示
	// -16 的 64 位补码 = 0xFFFFFFFFFFFFFFF0
	result, err := stackPointer.Call(h.ctx, uint64(0xFFFFFFFFFFFFFFF0))
	if err != nil {
		return 0, fmt.Errorf("failed to adjust stack pointer: %w", err)
	}

	// 获取返回指针（retptr）- 这是新的栈指针值
	// result[0] 是 uint64，需要转换为 uint32（WASM 内存地址是 32 位）
	retptr := uint32(result[0])

	// 确保在函数结束时恢复栈指针
	defer func() {
		_, _ = stackPointer.Call(h.ctx, uint64(16))
	}()

	// 写入 challenge 到内存
	challengePtr, challengeLen, err := h.writeToMemory(challenge)
	if err != nil {
		return 0, fmt.Errorf("failed to write challenge: %w", err)
	}

	// 写入 prefix 到内存
	prefixPtr, prefixLen, err := h.writeToMemory(prefix)
	if err != nil {
		return 0, fmt.Errorf("failed to write prefix: %w", err)
	}

	// 获取 wasm_solve 函数
	wasmSolve := h.instance.ExportedFunction("wasm_solve")
	if wasmSolve == nil {
		return 0, fmt.Errorf("wasm_solve function not found")
	}

	// 调用 wasm_solve
	// 函数签名：wasm_solve(retptr, challenge_ptr, challenge_len, prefix_ptr, prefix_len, difficulty)
	_, err = wasmSolve.Call(h.ctx,
		uint64(retptr),
		uint64(challengePtr),
		uint64(challengeLen),
		uint64(prefixPtr),
		uint64(prefixLen),
		api.EncodeF64(float64(difficulty)),
	)
	if err != nil {
		return 0, fmt.Errorf("failed to call wasm_solve: %w", err)
	}

	// 读取返回值
	// status 在 retptr:retptr+4 (int32, little-endian)
	// value 在 retptr+8:retptr+16 (float64, little-endian)
	mem := h.memory

	// 读取 status
	statusBytes, ok := mem.Read(retptr, 4)
	if !ok {
		return 0, fmt.Errorf("failed to read status from memory")
	}
	status := int32(binary.LittleEndian.Uint32(statusBytes))

	if status == 0 {
		return 0, fmt.Errorf("WASM solve returned status 0 (no solution)")
	}

	// 读取 value (float64)
	valueBytes, ok := mem.Read(retptr+8, 8)
	if !ok {
		return 0, fmt.Errorf("failed to read value from memory")
	}
	value := binary.LittleEndian.Uint64(valueBytes)
	floatValue := math.Float64frombits(value)

	return int(floatValue), nil
}

// SolveChallenge 解决 PoW 挑战并返回编码后的响应
func (p *DeepSeekPOW) SolveChallenge(config ChallengeConfig) (string, error) {
	answer, err := p.hasher.calculateHash(
		config.Algorithm,
		config.Challenge,
		config.Salt,
		config.Difficulty,
		config.ExpireAt,
	)
	if err != nil {
		return "", fmt.Errorf("failed to calculate hash: %w", err)
	}

	// 构建结果
	result := map[string]interface{}{
		"algorithm":   config.Algorithm,
		"challenge":   config.Challenge,
		"salt":        config.Salt,
		"answer":      answer,
		"signature":   config.Signature,
		"target_path": config.TargetPath,
	}

	// 编码为 JSON
	jsonData, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("failed to marshal result: %w", err)
	}

	// Base64 编码
	encoded := base64.StdEncoding.EncodeToString(jsonData)
	return encoded, nil
}

// Close 清理资源
func (p *DeepSeekPOW) Close() error {
	if p.hasher != nil {
		return p.hasher.Close()
	}
	return nil
}

// Close 清理哈希计算器资源
func (h *DeepSeekHash) Close() error {
	if h.instance != nil {
		if err := h.instance.Close(h.ctx); err != nil {
			return err
		}
	}
	if h.runtime != nil {
		return h.runtime.Close(h.ctx)
	}
	return nil
}

// FindWASMPath 查找 WASM 文件路径（用于自定义 WASM 文件）
// 默认情况下，WASM 文件已经嵌入到二进制中，不需要从文件系统查找
// 此函数仅用于需要从文件系统加载自定义 WASM 文件的场景
func FindWASMPath() (string, error) {
	wasmFileName := "sha3_wasm_bg.7b9ca65ddd.wasm"

	// 可能的路径列表
	possiblePaths := []string{
		// 从当前包目录查找（最可能的位置）
		func() string {
			_, filename, _, _ := runtime.Caller(0)
			dir := filepath.Dir(filename)
			return filepath.Join(dir, "wasm", wasmFileName)
		}(),
		// 从工作目录查找
		filepath.Join("dsk", "wasm", wasmFileName),
		// 从上级目录查找
		filepath.Join("..", "dsk", "wasm", wasmFileName),
		// 从项目根目录查找
		filepath.Join(".", "dsk", "wasm", wasmFileName),
	}

	// 尝试每个路径
	for _, path := range possiblePaths {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}

	return "", fmt.Errorf("WASM file not found. Tried paths: %v", possiblePaths)
}

// GetDefaultWASMPath 获取默认的 WASM 文件路径（已废弃，使用 FindWASMPath）
// Deprecated: Use FindWASMPath instead
func GetDefaultWASMPath() string {
	path, _ := FindWASMPath()
	return path
}
