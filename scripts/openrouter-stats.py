#!/usr/bin/env python3
import json
import math
import re
import statistics
import sys
from collections import Counter
from decimal import Decimal

ansi = re.compile(r"\x1b\[[0-9;]*m")
decoder = json.JSONDecoder(parse_float=Decimal)
records = {}
failures = 0
for raw in sys.stdin:
    try:
        envelope = json.loads(raw)
        line = envelope.get("log", raw) if isinstance(envelope, dict) else raw
    except (ValueError, TypeError):
        line = raw
    line = ansi.sub("", line)
    if 'msg="OpenRouter request failed"' in line or '"msg":"OpenRouter request failed"' in line:
        failures += 1
    if "OpenRouter usage metadata" not in line and "OpenRouter generation metadata" not in line:
        continue
    try:
        entry = json.loads(line, parse_float=Decimal)
    except ValueError:
        entry = {}
        for key in ("generation_id", "usage", "generation", "duration_ms", "finish_reason", "served_provider"):
            match = re.search(r"(?:^|\s)" + key + r"=", line)
            if match:
                try:
                    entry[key], _ = decoder.raw_decode(line[match.end():])
                except ValueError:
                    pass
    generation_id = entry.get("generation_id")
    if generation_id:
        if "OpenRouter usage metadata" in line:
            entry["_completion_seen"] = True
        if entry.get("generation") is None:
            entry.pop("generation", None)
        records.setdefault(generation_id, {}).update(entry)

records = {key: value for key, value in records.items() if value.get("_completion_seen")}
cache_requests = cache_hits = cache_prompt = cached = prompt = completion = reasoning = writes = 0
cost = Decimal(0)
cost_requests = usage_requests = generation_requests = 0
providers = Counter()
finishes = Counter()
durations = []
cache_discounts = Decimal(0)
for entry in records.values():
    usage = entry.get("usage") or {}
    generation = entry.get("generation") or {}
    generation_requests += bool(generation)
    providers[generation.get("provider_name") or entry.get("served_provider") or "unknown"] += 1
    finishes[entry.get("finish_reason", "unknown")] += 1
    if usage:
        usage_requests += 1
        count = usage.get("prompt_tokens", 0)
        prompt += count
        completion += usage.get("completion_tokens", 0)
        reasoning += (usage.get("completion_tokens_details") or {}).get("reasoning_tokens", 0)
        details = usage.get("prompt_tokens_details") or {}
        if details.get("cached_tokens") is not None:
            cache_requests += 1
            cache_prompt += count
            hit = details["cached_tokens"]
            cached += hit
            cache_hits += hit > 0
        writes += details.get("cache_write_tokens", 0) or 0
    charge = usage.get("cost")
    if charge is None:
        charge = generation.get("total_cost")
    if charge is not None:
        cost_requests += 1
        cost += Decimal(str(charge))
    if generation.get("cache_discount") is not None:
        cache_discounts += Decimal(str(generation["cache_discount"]))
    if entry.get("duration_ms") is not None:
        durations.append(float(entry["duration_ms"]))

def fraction(numerator, denominator):
    return numerator / denominator if denominator else None

def percentile(values, percent):
    return sorted(values)[max(0, math.ceil(percent * len(values)) - 1)] if values else None

json.dump({
    "completed_requests": len(records), "failed_requests": failures,
    "usage_observed_requests": usage_requests, "generation_observed_requests": generation_requests,
    "providers": dict(providers), "finish_reasons": dict(finishes),
    "prompt_tokens": prompt, "completion_tokens": completion, "reasoning_tokens": reasoning,
    "cached_tokens": cached, "cache_write_tokens": writes,
    "cache_observed_requests": cache_requests, "cache_hit_requests": cache_hits,
    "cache_hit_request_rate": fraction(cache_hits, cache_requests),
    "cached_input_token_rate": fraction(cached, cache_prompt),
    "cost_observed_requests": cost_requests, "cost_usd": str(cost),
    "cost_per_request_usd": str(cost / cost_requests) if cost_requests else None,
    "reported_cache_discount_usd": str(cache_discounts),
    "duration_mean_ms": statistics.mean(durations) if durations else None,
    "duration_p50_ms": percentile(durations, .5), "duration_p95_ms": percentile(durations, .95),
}, sys.stdout, indent=2)
sys.stdout.write("\n")
