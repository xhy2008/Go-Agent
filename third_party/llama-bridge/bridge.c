// llama.cpp embedding bridge - compiled to a shared library (DLL on Windows).
// The Go side loads this DLL dynamically via syscall (only when an embedding
// model is configured) and calls the few exported functions below, so the
// complex llama C API stays inside this C file.
//
// Build (Windows, MinGW gcc):
//   gcc -shared -O2 -o llama_bridge.dll bridge.c ^
//       -I<repo>/third_party/llama.cpp/include -I<repo>/third_party/llama.cpp/ggml/include ^
//       <repo>/third_party/llama.cpp/build/src/libllama.a ^
//       <repo>/third_party/llama.cpp/build/ggml/src/ggml-cpu.a ^
//       <repo>/third_party/llama.cpp/build/ggml/src/ggml.a ^
//       <repo>/third_party/llama.cpp/build/ggml/src/ggml-base.a ^
//       -lstdc++ -lm -fopenmp
#include <stdbool.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "llama.h"
#include "ggml.h"   // ggml_log_set（静默 ggml/后端日志，含 Vulkan 探测信息）

#ifdef _WIN32
#define CG_EXPORT __declspec(dllexport)
#else
#define CG_EXPORT __attribute__((visibility("default")))
#endif

// no-op 日志回调：llama.cpp / ggml 的全部日志（模型加载、后端探测、推理等）
// 都重定向到这里丢弃，避免启动时向控制台刷屏。
static void cg_silent_log(enum ggml_log_level level, const char * text, void * user_data) {
    (void) level; (void) text; (void) user_data;
}

// opaque handle returned to Go: model + context + error buffer
typedef struct {
    struct llama_model   * model;
    struct llama_context * ctx;
    const struct llama_vocab * vocab;
    int    n_embd;
    int    n_ubatch; // 每次 decode 允许的最大 token 数（encoder 整批必须 ≤ n_ubatch）
    enum llama_pooling_type pooling;
    char   error[512];
} cg_embed_handle;

static void set_error(cg_embed_handle * h, const char * msg) {
    snprintf(h->error, sizeof(h->error), "%s", msg);
}

// append a token to a llama_batch (seq 0)
static void batch_add(struct llama_batch * batch, llama_token tok, int32_t pos, bool logits) {
    int i = batch->n_tokens;
    batch->token[i] = tok;
    batch->pos[i] = pos;
    batch->n_seq_id[i] = 1;
    batch->seq_id[i][0] = 0;
    batch->logits[i] = logits;
    batch->n_tokens += 1;
}

CG_EXPORT void * cg_embed_new(const char * path, int pooling) {
    // 静默 llama.cpp / ggml 日志（在初始化后端与加载模型之前设置，覆盖全部后续输出）
    llama_log_set(cg_silent_log, NULL);
    ggml_log_set(cg_silent_log, NULL);

    llama_backend_init();

    cg_embed_handle * h = calloc(1, sizeof(cg_embed_handle));
    if (!h) return NULL;

    struct llama_model_params mp = llama_model_default_params();
    h->model = llama_model_load_from_file(path, mp);
    if (!h->model) {
        set_error(h, "model load failed");
        return h;
    }

    struct llama_context_params cp = llama_context_default_params();
    cp.embeddings  = true; // extract embeddings
    cp.n_ctx       = 8192;
    cp.n_batch     = 2048; // encoder 模型整批必须 ≤ n_ubatch，不能拆批，故直接开大
    cp.n_ubatch    = 2048;
    cp.n_threads       = 8;
    cp.n_threads_batch = 8;
    // 多序列批量编码（cg_embed_encode_batch）需要统一 KV 缓冲：n_seq_max 上限放宽到
    // LLAMA_MAX_SEQ，否则 batch 里 seq_id > 0 会被判为非法。encoder-only 模型无 KV 缓存，
    // 此开关只影响 batch 校验，不影响单条 encode。
    cp.kv_unified = true;
    cp.n_seq_max  = 256;
    if (pooling != -1) {
        cp.pooling_type = (enum llama_pooling_type) pooling;
    }
    h->ctx = llama_init_from_model(h->model, cp);
    if (!h->ctx) {
        set_error(h, "context init failed");
        llama_model_free(h->model);
        h->model = NULL;
        return h;
    }

    h->vocab  = llama_model_get_vocab(h->model);
    h->n_embd = (int) llama_model_n_embd_out(h->model);
    h->n_ubatch = (int) cp.n_ubatch;
    h->pooling = (enum llama_pooling_type) pooling;
    if (h->n_embd == 0) {
        set_error(h, "model has no embedding output");
        return h;
    }
    return h;
}

CG_EXPORT int cg_embed_dim(void * p) {
    cg_embed_handle * h = (cg_embed_handle *) p;
    return h ? h->n_embd : 0;
}

CG_EXPORT int cg_embed_error(void * p, char * buf, int buflen) {
    cg_embed_handle * h = (cg_embed_handle *) p;
    if (!h || !buf || buflen <= 0) return 0;
    int n = (int) strlen(h->error);
    if (n > buflen - 1) n = buflen - 1;
    memcpy(buf, h->error, (size_t) n);
    buf[n] = '\0';
    return n;
}

// encodes text into out (caller-allocated, maxlen floats); returns number of
// floats written (= dim) on success, 0 on failure (error via cg_embed_error).
CG_EXPORT int cg_embed_encode(void * p, const char * text, float * out, int maxlen) {
    cg_embed_handle * h = (cg_embed_handle *) p;
    if (!h || !h->ctx || !h->vocab) return 0;

    const char * src = text ? text : "";
    int text_len = (int) strlen(src);

    // first call returns required buffer size (negative) or 0 if empty
    int32_t need = llama_tokenize(h->vocab, src, text_len, NULL, 0, true, false);
    if (need == 0) {
        set_error(h, "empty tokenization");
        return 0;
    }
    if (need < 0) need = -need;

    llama_token * toks = malloc((size_t) need * sizeof(llama_token));
    if (!toks) { set_error(h, "token alloc failed"); return 0; }
    int32_t n = llama_tokenize(h->vocab, src, text_len, toks, need, true, false);
    if (n <= 0) {
        set_error(h, "tokenize failed");
        free(toks);
        return 0;
    }

    llama_memory_clear(llama_get_memory(h->ctx), true);

    struct llama_batch batch = llama_batch_init(n, 0, 1);
    for (int32_t i = 0; i < n; i++) {
        batch_add(&batch, toks[i], i, true);
    }
    free(toks);

    int rc = llama_decode(h->ctx, batch);
    llama_batch_free(batch);
    if (rc != 0) {
        set_error(h, "llama_decode failed");
        return 0;
    }

    float * embd = NULL;
    if (h->pooling == LLAMA_POOLING_TYPE_NONE) {
        embd = llama_get_embeddings_ith(h->ctx, 0);
    } else {
        embd = llama_get_embeddings_seq(h->ctx, 0);
    }
    if (!embd) {
        set_error(h, "no embeddings returned");
        return 0;
    }

    int cnt = h->n_embd;
    if (cnt > maxlen) cnt = maxlen;
    memcpy(out, embd, (size_t) cnt * sizeof(float));
    return cnt;
}

// encodes a batch of texts in ONE decode pass: each text is a separate sequence
// (seq_id = t), positions restart at 0, logits on the last token of each sequence
// (池化模型从该 token 取序列 embedding). This amortizes per-call graph setup /
// kernel-launch overhead — for many short texts it is several times faster than
// calling cg_embed_encode once per text (LLAMA_MAX_SEQ=256 limits batch width).
// out is caller-allocated, n_texts*dim floats. Returns n_texts on success, 0 on error.
CG_EXPORT int cg_embed_encode_batch(void * p, const char ** texts, const int * lens, int n_texts, float * out, int dim) {
    cg_embed_handle * h = (cg_embed_handle *) p;
    if (!h || !h->ctx || !h->vocab || !texts || n_texts <= 0) return 0;

    int * counts = calloc((size_t) n_texts, sizeof(int));
    if (!counts) { set_error(h, "alloc failed"); return 0; }
    int total = 0;
    int n_seq = 0; // 非空文本条数（空文本 → 零向量，不入 batch）
    for (int t = 0; t < n_texts; t++) {
        const char * src = texts[t] ? texts[t] : "";
        int len = lens ? lens[t] : (int) strlen(src);
        if (len == 0) continue;
        int32_t need = llama_tokenize(h->vocab, src, len, NULL, 0, true, false);
        if (need < 0) need = -need;
        counts[t] = need;
        total += need;
        n_seq++;
    }
    if (total == 0) {
        for (int t = 0; t < n_texts; t++) {
            memset(out + (size_t) t * dim, 0, (size_t) dim * sizeof(float));
        }
        free(counts);
        return n_texts;
    }
    if (n_seq > 256) { // llama.cpp 内部上限 LLAMA_MAX_SEQ（未在公开头文件导出）
        free(counts);
        set_error(h, "too many sequences per batch");
        return 0;
    }

    llama_token * toks = malloc((size_t) total * sizeof(llama_token));
    int * offs = malloc((size_t) (n_texts + 1) * sizeof(int));
    if (!toks || !offs) {
        free(counts); free(toks); free(offs);
        set_error(h, "alloc failed");
        return 0;
    }
    offs[0] = 0;
    int off = 0;
    for (int t = 0; t < n_texts; t++) {
        if (counts[t] == 0) { offs[t+1] = off; continue; }
        const char * src = texts[t];
        int len = lens ? lens[t] : (int) strlen(src);
        int32_t n = llama_tokenize(h->vocab, src, len, toks + off, counts[t], true, false);
        if (n < 0) n = 0;
        offs[t+1] = off + n;
        off += n;
    }

    llama_memory_clear(llama_get_memory(h->ctx), true);

    // encoder 模型无法拆批：整批 token 必须 ≤ n_ubatch（llama-context.cpp 内
    // GGML_ASSERT(cparams.n_ubatch >= n_tokens)）。因此把文本切成多个 pass，
    // 每个 pass 累积到预算上限再 decode 一次；每条文本一个独立 seq，池化后逐条写出。
    int t = 0;
    while (t < n_texts) {
        while (t < n_texts && counts[t] == 0) t++; // 空文本：零向量已填，跳过
        if (t >= n_texts) break;
        int begin = t;
        int pass = 0;
        while (t < n_texts && counts[t] > 0) {
            if (pass + counts[t] > h->n_ubatch) break;
            pass += counts[t];
            t++;
        }
        if (t == begin) {
            // 单条超长文本（token 数 > n_ubatch）：退化为独立 decode
            int idx = begin;
            struct llama_batch b1 = llama_batch_init(counts[idx], 0, 1);
            for (int j = 0; j < counts[idx]; j++) {
                b1.token[j] = toks[offs[idx] + j];
                b1.pos[j] = j;
                b1.n_seq_id[j] = 1;
                b1.seq_id[j][0] = 0;
                b1.logits[j] = (j == counts[idx] - 1);
            }
            b1.n_tokens = counts[idx];
            int rc = llama_decode(h->ctx, b1);
            llama_batch_free(b1);
            if (rc != 0) {
                free(counts); free(toks); free(offs);
                set_error(h, "llama_decode failed");
                return 0;
            }
            const float * embd = (h->pooling == LLAMA_POOLING_TYPE_NONE)
                ? llama_get_embeddings_ith(h->ctx, counts[idx] - 1)
                : llama_get_embeddings_seq(h->ctx, 0);
            if (!embd) {
                free(counts); free(toks); free(offs);
                set_error(h, "no embeddings returned");
                return 0;
            }
            memcpy(out + (size_t) idx * dim, embd, (size_t) dim * sizeof(float));
            t++;
            continue;
        }
        // 正常 pass：texts [begin, t)，本地 seq 从 0 起
        int ns = t - begin;
        struct llama_batch batch = llama_batch_init(pass, 0, ns);
        int tok = 0;
        for (int tt = begin; tt < t; tt++) {
            int len_t = offs[tt+1] - offs[tt];
            for (int j = 0; j < len_t; j++) {
                batch.token[tok] = toks[offs[tt] + j];
                batch.pos[tok] = j;
                batch.n_seq_id[tok] = 1;
                batch.seq_id[tok][0] = (llama_seq_id)(tt - begin);
                batch.logits[tok] = true; // 末 token 的 embedding 由 llama_get_embeddings_seq 池化
                tok++;
            }
        }
        batch.n_tokens = tok; // 直接写 batch 数组时需显式设置 token 数
        int rc = llama_decode(h->ctx, batch);
        llama_batch_free(batch);
        if (rc != 0) {
            free(counts); free(toks); free(offs);
            set_error(h, "llama_decode failed");
            return 0;
        }
        for (int tt = begin; tt < t; tt++) {
            const float * embd = NULL;
            if (h->pooling == LLAMA_POOLING_TYPE_NONE) {
                // 非池化：取该序列最后一个 token 的 embedding（batch 下标定位）
                int idx = (offs[tt+1] - offs[begin]) - 1;
                embd = llama_get_embeddings_ith(h->ctx, idx);
            } else {
                embd = llama_get_embeddings_seq(h->ctx, tt - begin);
            }
            if (!embd) {
                free(counts); free(toks); free(offs);
                set_error(h, "no embeddings returned");
                return 0;
            }
            memcpy(out + (size_t) tt * dim, embd, (size_t) dim * sizeof(float));
        }
    }
    free(counts); free(toks); free(offs);
    return n_texts;
}

CG_EXPORT void cg_embed_free(void * p) {
    cg_embed_handle * h = (cg_embed_handle *) p;
    if (!h) return;
    if (h->ctx)   llama_free(h->ctx);
    if (h->model) llama_model_free(h->model);
    free(h);
}
