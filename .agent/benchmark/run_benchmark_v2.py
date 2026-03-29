"""Benchmark v2: actionability-based scoring with prompt variants."""
import json
import time
import os
import argparse
import http.client
import concurrent.futures
from pathlib import Path
from urllib.parse import urlparse
from prompts import VARIANTS

BENCHMARK_DIR = Path(__file__).parent
FRAGMENTS_FILE = BENCHMARK_DIR / "test_fragments_v2.json"
RESULTS_DIR = BENCHMARK_DIR / "results_v2"

LM_STUDIO_URL = os.getenv("LM_STUDIO_URL", "http://172.20.23.32:1234")
_parsed = urlparse(LM_STUDIO_URL)
_HOST = _parsed.hostname or "localhost"
_PORT = _parsed.port or 1234

MODELS = [
    "qwen/qwen3-8b",
    "qwen/qwen3.5-9b",
    "deepseek-coder-v2-lite-instruct",
    "openai/gpt-oss-20b",
    "huihui-qwen3.5-9b-claude-4.6-opus-abliterated",
    "qwen3.5-9b-claude-code",
    "qwen3.5-9b-uncensored-hauhaucs-aggressive",
    "qwen/qwen3.5-35b-a3b",
    "minimax-m2.5",
    "qwen/qwen3-coder-next",
    "zai-org/glm-4.7-flash",
    "mistralai/devstral-small-2507",
    "qwen3-coder-30b-a3b-instruct-1m",
]


def call_llm(model, system, user, timeout=300):
    payload = json.dumps({
        "model": model,
        "messages": [
            {"role": "system", "content": system},
            {"role": "user", "content": user},
        ],
        "temperature": 0.3,
        "max_tokens": 4096,
    })
    start = time.monotonic()
    try:
        conn = http.client.HTTPConnection(_HOST, _PORT, timeout=timeout)
        conn.request("POST", "/v1/chat/completions", body=payload,
                     headers={"Content-Type": "application/json"})
        resp = conn.getresponse()
        data = json.loads(resp.read().decode())
        conn.close()
        elapsed = time.monotonic() - start
        msg = data["choices"][0]["message"]
        content = msg.get("content", "") or ""
        reasoning = msg.get("reasoning_content", "") or ""
        text = (reasoning + "\n" + content).strip() if reasoning else content
        return text, elapsed
    except Exception as e:
        return f"ERROR: {e}", time.monotonic() - start


def unload_model(model):
    try:
        conn = http.client.HTTPConnection(_HOST, _PORT, timeout=30)
        conn.request("POST", "/api/v1/models/unload",
                     body=json.dumps({"instance_id": model}),
                     headers={"Content-Type": "application/json"})
        resp = conn.getresponse()
        resp.read()
        conn.close()
        print(f"  Unloaded: {model}")
    except Exception as e:
        print(f"  Unload error: {e}")


def evaluate_v2(response, fragment):
    """Actionability-based scoring."""
    expected = fragment.get("expected", {})
    is_noise = expected.get("should_be_no_obs", False)
    expected_cat = expected.get("category")
    expected_keywords = expected.get("keywords", [])

    # Parse response
    has_no_obs = "<no_observations_found" in response
    has_obs = "<observation>" in response or "<observation " in response
    obs_count = response.count("<observation>")
    has_category = "<category>" in response
    is_json = response.strip().startswith("{")

    # For JSON variant
    if is_json:
        try:
            j = json.loads(response.strip())
            obs_list = j.get("observations", [])
            obs_count = len(obs_list)
            has_obs = obs_count > 0
            has_no_obs = obs_count == 0
            has_category = any("category" in o for o in obs_list)
        except json.JSONDecodeError:
            pass

    # Extract category from response
    detected_cat = None
    for cat in ["user_behavior", "decision", "correction", "debugging", "gotcha", "pattern"]:
        if f"<category>{cat}</category>" in response or f'"category": "{cat}"' in response:
            detected_cat = cat
            break

    score = 0

    # FORMAT (max 2)
    if has_obs or has_no_obs:
        score += 1  # Valid structure
    if has_category or is_json:
        score += 1  # Uses categories

    # NOISE REJECTION (max 2, critical)
    if is_noise:
        if has_no_obs and not has_obs:
            score += 2  # Correctly rejected noise
        elif has_obs:
            score -= 2  # False positive on noise

    # ACTIONABILITY (max 3)
    if not is_noise and has_obs:
        # Check for specific references (files, errors, functions)
        lower = response.lower()
        specifics = sum(1 for w in [".go", ".js", ".py", ".ts", "func ", "error:", "http://", "api/"]
                       if w in lower)
        if specifics >= 2:
            score += 1  # Has specific references

        # Check narrative explains WHY
        why_words = ["because", "reason", "chose", "instead", "trigger", "rule", "root cause", "fix"]
        why_count = sum(1 for w in why_words if w in lower)
        if why_count >= 2:
            score += 1  # Explains rationale

        # Check category accuracy
        if expected_cat and detected_cat == expected_cat:
            score += 1  # Correct category

    # CORRECTION DETECTION (max 3, correction fragments only)
    if fragment.get("type") == "correction":
        if detected_cat == "user_behavior":
            # Check trigger specificity
            if "trigger" in response.lower() and "rule" in response.lower():
                score += 3  # Full TRIGGER/RULE/REASON
            else:
                score += 2  # Detected as user_behavior but no structure
        elif detected_cat == "correction":
            score += 1  # Close but wrong category
        elif has_obs and any(kw in response.lower() for kw in expected_keywords):
            score += 1  # Found relevant content

    # PENALTIES
    if obs_count > 2:
        score -= 1  # Violated limit
    narrative_words = len(response.split())
    # Only penalize if output is clearly too verbose (>500 words of actual content, not thinking)
    if narrative_words > 800:
        score -= 1

    # Normalize to 0-10
    max_possible = 8 if fragment.get("type") == "correction" else 5 if is_noise else 5
    normalized = max(0, min(10, int(score / max(max_possible, 1) * 10)))

    return {
        "quality_score": normalized,
        "raw_score": score,
        "max_possible": max_possible,
        "has_obs": has_obs,
        "has_no_obs": has_no_obs,
        "obs_count": obs_count,
        "has_category": has_category,
        "detected_cat": detected_cat,
        "expected_cat": expected_cat,
        "is_noise": is_noise,
        "noise_correct": is_noise and has_no_obs and not has_obs,
        "response_preview": response[:300],
    }


def run_fragment(model, fragment, prompt_variant):
    sys_prompt, user_template = VARIANTS[prompt_variant]
    user_prompt = user_template.format(
        project=fragment.get("project", ""),
        exchanges=fragment.get("exchange_count", 5),
        transcript=fragment.get("transcript", "")[:3000],
    )
    response, elapsed = call_llm(model, sys_prompt, user_prompt)
    evaluation = evaluate_v2(response, fragment)
    return {
        "fragment_id": fragment["id"],
        "fragment_type": fragment["type"],
        "model": model,
        "prompt": prompt_variant,
        "elapsed_s": round(elapsed, 2),
        **evaluation,
    }


def benchmark_combination(model, prompt_variant, fragments, parallel=4):
    """Run one model+prompt combination on all fragments."""
    results = []
    with concurrent.futures.ThreadPoolExecutor(max_workers=parallel) as executor:
        futures = {
            executor.submit(run_fragment, model, frag, prompt_variant): frag
            for frag in fragments
        }
        for future in concurrent.futures.as_completed(futures):
            try:
                r = future.result()
                results.append(r)
                mark = "OK" if r["quality_score"] >= 5 else "LOW" if r["quality_score"] >= 3 else "FAIL"
                print(f"    {mark:4s} {r['fragment_id']:20s} q={r['quality_score']:2d}/10 {r['elapsed_s']:5.1f}s cat={r['detected_cat'] or '-'}")
            except Exception as e:
                frag = futures[future]
                results.append({"fragment_id": frag["id"], "model": model, "prompt": prompt_variant, "error": str(e)})
    return results


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--model", default="all")
    parser.add_argument("--prompt", default="all", help="Prompt variant: A_strict, B_fewshot, C_json, D_think, E_minimal, or all")
    parser.add_argument("--parallel", type=int, default=1,
                        help="Fragments per model in parallel. Default 1 — avoids loading multiple model instances in LM Studio")
    args = parser.parse_args()

    with open(FRAGMENTS_FILE, "r", encoding="utf-8") as f:
        fragments = json.load(f)
    print(f"Loaded {len(fragments)} test fragments")

    models = MODELS if args.model == "all" else [args.model.strip('"').strip("'")]
    prompts = list(VARIANTS.keys()) if args.prompt == "all" else [args.prompt]

    RESULTS_DIR.mkdir(exist_ok=True)
    all_summaries = []

    for model in models:
        print(f"\n{'='*60}")
        print(f"MODEL: {model}")
        print(f"{'='*60}")

        # Warmup
        print("  Warmup...")
        call_llm(model, "Reply: ok", "ok", timeout=180)
        print("  Ready.")

        model_results = []
        for pv in prompts:
            print(f"\n  Prompt: {pv}")
            results = benchmark_combination(model, pv, fragments, args.parallel)
            model_results.extend(results)

            # Summary for this combo
            valid = [r for r in results if "error" not in r]
            scores = [r["quality_score"] for r in valid]
            times = [r["elapsed_s"] for r in valid]
            noise_correct = sum(1 for r in valid if r.get("noise_correct", False))
            noise_total = sum(1 for r in valid if r.get("is_noise", False))

            summary = {
                "model": model,
                "prompt": pv,
                "avg_quality": round(sum(scores) / len(scores), 1) if scores else 0,
                "avg_time": round(sum(times) / len(times), 1) if times else 0,
                "noise_accuracy": f"{noise_correct}/{noise_total}",
                "correction_best": max((r["quality_score"] for r in valid if r.get("fragment_type") == "correction"), default=0),
            }
            all_summaries.append(summary)
            print(f"  -> quality={summary['avg_quality']}/10  time={summary['avg_time']}s  noise={summary['noise_accuracy']}")

        # Save per-model
        safe = model.replace("/", "_").replace("\\", "_").replace('"', "").replace("'", "")
        with open(RESULTS_DIR / f"{safe}.json", "w", encoding="utf-8") as f:
            json.dump(model_results, f, indent=2, ensure_ascii=False)

        # Unload
        unload_model(model)
        time.sleep(2)

    # Final comparison
    print(f"\n{'='*80}")
    print("COMPARISON TABLE (sorted by quality)")
    print(f"{'='*80}")
    print(f"{'Model':35s} {'Prompt':12s} {'Quality':>8s} {'Time':>6s} {'Noise':>6s} {'Corr':>5s}")
    print(f"{'-'*35} {'-'*12} {'-'*8} {'-'*6} {'-'*6} {'-'*5}")

    for s in sorted(all_summaries, key=lambda x: -x["avg_quality"]):
        print(f"{s['model']:35s} {s['prompt']:12s} {s['avg_quality']:>6.1f}/10 {s['avg_time']:>5.1f}s {s['noise_accuracy']:>6s} {s['correction_best']:>4d}/10")

    with open(RESULTS_DIR / "comparison_v2.json", "w") as f:
        json.dump(all_summaries, f, indent=2)
    print(f"\nResults saved to {RESULTS_DIR}")


if __name__ == "__main__":
    main()
