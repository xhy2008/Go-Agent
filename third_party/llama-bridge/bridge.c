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

#ifdef _WIN32
#define CG_EXPORT __declspec(dllexport)
#else
#define CG_EXPORT __attribute__((visibility("default")))
#endif

// opaque handle returned to Go: model + context + error buffer
typedef struct {
    struct llama_model   * model;
    struct llama_context * ctx;
    const struct llama_vocab * vocab;
    int    n_embd;
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
    cp.n_batch     = 256;
    cp.n_ubatch    = 256;
    cp.n_threads       = 8;
    cp.n_threads_batch = 8;
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

CG_EXPORT void cg_embed_free(void * p) {
    cg_embed_handle * h = (cg_embed_handle *) p;
    if (!h) return;
    if (h->ctx)   llama_free(h->ctx);
    if (h->model) llama_model_free(h->model);
    free(h);
}
