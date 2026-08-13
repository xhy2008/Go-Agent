package embed

// Pooling 是 embedding 的池化方式（数值与 llama_pooling_type 对齐，经 llama_bridge.dll 传递）。
type Pooling int

const (
	PoolUnspecified Pooling = iota - 1 // 由模型元数据决定
	PoolNone                           // 逐 token
	PoolMean                           // 平均池化
	PoolCLS                            // CLS 池化（bge 系列）
	PoolLast                           // 末位池化（nomic 系列）
)
