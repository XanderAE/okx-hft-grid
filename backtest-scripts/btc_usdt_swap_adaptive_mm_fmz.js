/*backtest
start: 2023-01-01 00:00:00
end: 2023-04-01 00:00:00
period: 1m
exchanges: [{"eid":"Futures_OKX","currency":"BTC_USDT","balance":10000}]
*/

// BTC-USDT-SWAP 自适应做市 FMZ 原生期货回测。
// 安全边界：main 启动及每个原生订单 API 包装器都会验证 IsVirtual() === true；禁止实盘运行。
// FMZ 的 1 分钟回测每根已完成 K 线只作一次决策，不能复现 Go 进程的 30 秒节奏。
// 平台成交、手续费和滑点由 FMZ 回测器撮合；脚本内费用与资金费仅用于独立策略诊断。

// ===== FMZ 可调回测参数 =====
var FastLength = 5;
var SlowLength = 20;
var SlopeBars = 1;
var MinConfidence = 0.0;
var VolatilityLength = 20;
var VolatilityPenalty = 40.0;
var MaxLeverage = 3.0;
var TickSize = 0.1;

var PaperEquity = 10000.0;
var RequestedNotionalUSDT = 50.0;
var MarginAllocationPercent = 10.0;
// OKX BTC-USDT-SWAP：ctVal=0.01 BTC/张，lotSz=0.01 张，minSz=0.01 张，tickSz=0.1。

var TakeProfitPercent = 0.08;        // 默认 0.08%，限制为 0.05%–0.08%
var StopLossPercent = 2.0;           // 默认 2.0%，限制为 1.5%–3.0%
var MaxHoldHours = 12.0;
var MakerFeePercent = 0.02;          // 仅内部诊断；FMZ 平台费用由回测 UI 决定
var TakerFeePercent = 0.05;          // 仅内部诊断；FMZ 平台费用由回测 UI 决定
var ForcedExitSlippagePercent = 0.05;
var FallbackFundingRate8hPercent = 0.01;

// 留空表示 AUTO：使用 FMZ 本次回测实际处理到的首尾 K 线，且所有 K 线都允许交易。
var InSampleStart = "";
var OOSStart = "";
var EndTime = "";
var TradeInSample = true;
var TradeOOS = true;
var PollMilliseconds = 1000;

var LONG = 1;
var SHORT = -1;
var FLAT = 0;
var HOUR_MS = 60 * 60 * 1000;
var FUNDING_INTERVAL_MS = 8 * HOUR_MS;

var cfg = null;
var state = null;
var virtualBacktestConfirmed = false;

function numberValue(value, fallback) {
    value = Number(value);
    return isFinite(value) ? value : fallback;
}

function boolValue(value) {
    return value === true || value === "true" || value === 1 || value === "1";
}

function clamp(value, low, high) {
    if (value < low) {
        return low;
    }
    if (value > high) {
        return high;
    }
    return value;
}

function floorToContractStep(value) {
    if (!(value > 0) || !isFinite(value)) {
        return 0;
    }
    var scaled = value * 100;
    var nearest = Math.round(scaled);
    var tolerance = 1e-10 * Math.max(1, Math.abs(scaled));
    if (Math.abs(scaled - nearest) <= tolerance) {
        scaled = nearest;
    }
    return Math.floor(scaled) / 100;
}

function normalizeContracts(value) {
    return floorToContractStep(numberValue(value, 0));
}

function roundPrice(value) {
    if (!(value > 0) || cfg === null) {
        return 0;
    }
    return Math.round(value / cfg.tickSize) * cfg.tickSize;
}

function targetPriceFor(position, mark, now) {
    var raw;
    var margin = targetMargin(position, mark, now);
    if (position.direction === LONG) {
        raw = position.entry * (1 + margin);
        return Math.ceil((raw - cfg.tickSize * 1e-9) / cfg.tickSize) * cfg.tickSize;
    }
    raw = position.entry * (1 - margin);
    return Math.floor((raw + cfg.tickSize * 1e-9) / cfg.tickSize) * cfg.tickSize;
}

function formatPrice(value) {
    if (!(value > 0) || cfg === null) {
        return "0";
    }
    var decimals = 0;
    var tick = cfg.tickSize;
    while (decimals < 8 && Math.abs(tick - Math.round(tick)) > 1e-10) {
        tick *= 10;
        decimals++;
    }
    return roundPrice(value).toFixed(decimals);
}

function formatContracts(value) {
    return normalizeContracts(value).toFixed(2);
}

function parseOptionalTime(value, parameterName) {
    if (value === null || typeof value === "undefined" || String(value).replace(/^\s+|\s+$/g, "") === "") {
        return 0;
    }
    var parsed = Date.parse(value);
    if (!isFinite(parsed) || parsed <= 0) {
        throw "日期参数无效：" + parameterName + " 必须为空或可解析的日期时间";
    }
    return parsed;
}

function formatTime(timestamp) {
    return timestamp > 0 && isFinite(timestamp) ? new Date(timestamp).toISOString() : "未设置";
}

function directionName(direction) {
    if (direction === LONG) {
        return "LONG";
    }
    if (direction === SHORT) {
        return "SHORT";
    }
    return "FLAT";
}

// 此检查必须位于每个订单设置、查询、撤销和下单 API 之前，避免任何旁路误触实盘。
function requireVirtualBacktest() {
    if (typeof IsVirtual !== "function" || IsVirtual() !== true) {
        throw "安全拒绝：本策略仅允许在 FMZ 回测环境运行（必须满足 IsVirtual() === true），已禁止任何合约设置或下单操作。";
    }
    virtualBacktestConfirmed = true;
}

function nativeSetContractType() {
    requireVirtualBacktest();
    var result;
    try {
        result = exchange.SetContractType("swap");
    } catch (error) {
        throw "FMZ 适配器不支持 BTC-USDT 永续 swap 合约：" + String(error);
    }
    if (result === false || result === null || typeof result === "undefined") {
        throw "FMZ 适配器不支持 BTC-USDT 永续 swap 合约，SetContractType(\"swap\") 失败。";
    }
    return result;
}

function nativeSetMarginLevel(leverage) {
    requireVirtualBacktest();
    if (typeof exchange.SetMarginLevel !== "function") {
        return true;
    }
    try {
        exchange.SetMarginLevel(leverage);
        return true;
    } catch (error) {
        Log("[NATIVE-ERROR] SetMarginLevel", leverage, "失败", String(error));
        return false;
    }
}

function nativeSetDirection(direction) {
    requireVirtualBacktest();
    return exchange.SetDirection(direction);
}

function nativeBuy(price, contracts) {
    requireVirtualBacktest();
    return exchange.Buy(price, contracts);
}

function nativeSell(price, contracts) {
    requireVirtualBacktest();
    return exchange.Sell(price, contracts);
}

function nativeGetOrder(id) {
    requireVirtualBacktest();
    try {
        return exchange.GetOrder(id);
    } catch (error) {
        Log("[NATIVE-ERROR] GetOrder id=" + id, String(error));
        return null;
    }
}

function nativeCancelOrder(id) {
    requireVirtualBacktest();
    try {
        return exchange.CancelOrder(id);
    } catch (error) {
        Log("[NATIVE-ERROR] CancelOrder id=" + id, String(error));
        return false;
    }
}

function orderIdFrom(result) {
    if (result === null || typeof result === "undefined" || result === false) {
        return null;
    }
    if (typeof result === "object") {
        if (result.Id !== null && typeof result.Id !== "undefined") {
            return result.Id;
        }
        if (result.ID !== null && typeof result.ID !== "undefined") {
            return result.ID;
        }
    }
    return result;
}

function orderStatus(order) {
    if (!order) {
        return null;
    }
    if (order.Status !== null && typeof order.Status !== "undefined") {
        return order.Status;
    }
    if (order.State !== null && typeof order.State !== "undefined") {
        return order.State;
    }
    return null;
}

function statusText(order) {
    var status = orderStatus(order);
    return status === null ? "UNKNOWN" : String(status).toUpperCase();
}

function isOrderClosed(order) {
    var status = orderStatus(order);
    if (typeof ORDER_STATE_CLOSED !== "undefined" && status === ORDER_STATE_CLOSED) {
        return true;
    }
    var text = statusText(order);
    if (text === "CLOSED" || text === "FILLED" || text === "DONE" || text === "COMPLETED") {
        return true;
    }
    return typeof status === "number" && status === 1;
}

function isOrderCanceled(order) {
    var status = orderStatus(order);
    if (typeof ORDER_STATE_CANCELED !== "undefined" && status === ORDER_STATE_CANCELED) {
        return true;
    }
    var text = statusText(order);
    if (text === "CANCELED" || text === "CANCELLED" || text === "REJECTED" || text === "EXPIRED") {
        return true;
    }
    return typeof status === "number" && status === 2;
}

function isOrderPending(order) {
    var status = orderStatus(order);
    if (typeof ORDER_STATE_PENDING !== "undefined" && status === ORDER_STATE_PENDING) {
        return true;
    }
    var text = statusText(order);
    if (text === "PENDING" || text === "OPEN" || text === "NEW" || text === "PARTIAL" || text === "PARTIALLY_FILLED") {
        return true;
    }
    return status === null || (typeof status === "number" && status === 0);
}

function orderAmount(order) {
    return order ? normalizeContracts(order.Amount) : 0;
}

function orderDealAmount(order) {
    if (!order) {
        return 0;
    }
    var dealt = normalizeContracts(order.DealAmount);
    if (dealt >= 0.01) {
        return dealt;
    }
    // FMZ 某些适配器在 CLOSED 状态不提供 DealAmount，此时才允许以 Amount 回退。
    return isOrderClosed(order) ? orderAmount(order) : 0;
}

function orderFillPrice(order, fallback) {
    if (order) {
        var average = numberValue(order.AvgPrice, 0);
        if (average > 0) {
            return roundPrice(average);
        }
        var price = numberValue(order.Price, 0);
        if (price > 0) {
            return roundPrice(price);
        }
    }
    return roundPrice(fallback);
}

function buildConfig() {
    var maxLeverage = clamp(numberValue(MaxLeverage, 1), 1, 3);
    var tp = clamp(numberValue(TakeProfitPercent, 0.08) / 100, 0.0005, 0.0008);
    var stop = clamp(numberValue(StopLossPercent, 2.0) / 100, 0.015, 0.03);
    var maxHoldHours = clamp(numberValue(MaxHoldHours, 12), 0.01, 12);
    var start = parseOptionalTime(InSampleStart, "InSampleStart");
    var oos = parseOptionalTime(OOSStart, "OOSStart");
    var end = parseOptionalTime(EndTime, "EndTime");
    if (start > 0 && oos > 0 && oos <= start) {
        throw "日期参数无效：填写 OOSStart 时必须晚于 InSampleStart";
    }
    if (end > 0 && start > 0 && end <= start) {
        throw "日期参数无效：EndTime 必须晚于 InSampleStart";
    }
    if (end > 0 && oos > 0 && end <= oos) {
        throw "日期参数无效：EndTime 必须晚于 OOSStart";
    }
    return {
        fastLength: Math.max(2, Math.floor(numberValue(FastLength, 5))),
        slowLength: Math.max(20, Math.floor(numberValue(SlowLength, 20))),
        slopeBars: clamp(Math.floor(numberValue(SlopeBars, 1)), 1, 5),
        minConfidence: clamp(numberValue(MinConfidence, 0.0), 0, 1),
        volatilityLength: Math.max(2, Math.floor(numberValue(VolatilityLength, 20))),
        volatilityPenalty: Math.max(0, numberValue(VolatilityPenalty, 40)),
        maxLeverage: maxLeverage,
        tickSize: Math.max(0.00000001, numberValue(TickSize, 0.1)),
        initialEquity: Math.max(0, numberValue(PaperEquity, 10000)),
        requestedNotionalUSDT: Math.max(0, numberValue(RequestedNotionalUSDT, 50.0)),
        marginAllocation: clamp(numberValue(MarginAllocationPercent, 10) / 100, 0, 1),
        contractBTC: 0.01,
        contractLotSize: 0.01,
        minContracts: 0.01,
        tp: tp,
        stop: stop,
        maxHoldMs: maxHoldHours * HOUR_MS,
        makerFee: Math.max(0, numberValue(MakerFeePercent, 0.02) / 100),
        takerFee: Math.max(0, numberValue(TakerFeePercent, 0.05) / 100),
        slippage: Math.max(0, numberValue(ForcedExitSlippagePercent, 0.05) / 100),
        fallbackFundingRate: numberValue(FallbackFundingRate8hPercent, 0.01) / 100,
        dateMode: start === 0 && oos === 0 && end === 0 ? "AUTO" : "MANUAL",
        inSampleStart: start,
        oosStart: oos,
        endTime: end,
        tradeInSample: boolValue(TradeInSample),
        tradeOOS: boolValue(TradeOOS)
    };
}

function newState(initialEquity) {
    return {
        cash: initialEquity,
        peakEquity: initialEquity,
        maxDrawdown: 0,
        firstProcessedTime: 0,
        lastProcessedTime: 0,
        closes: [],
        pending: null,
        position: null,
        lastNativeExit: null,
        warmupLogged: false,
        finalLogged: false,
        rawSignals: 0,
        qualifiedSignals: 0,
        skippedSignals: 0,
        entryFills: 0,
        entryCancels: 0,
        longEntries: 0,
        shortEntries: 0,
        trades: 0,
        wins: 0,
        hardStops: 0,
        forcedExits: 0,
        takeProfits: 0,
        liquidationEvents: 0,
        fees: 0,
        funding: 0,
        fundingEvents: 0,
        inSampleTrades: 0,
        oosTrades: 0,
        inSamplePnL: 0,
        oosPnL: 0,
        lastClose: 0
    };
}

function phaseFor(timestamp) {
    if (cfg.inSampleStart > 0 && timestamp < cfg.inSampleStart) {
        return "OUT";
    }
    if (cfg.endTime > 0 && timestamp >= cfg.endTime) {
        return "OUT";
    }
    if (cfg.oosStart > 0 && timestamp >= cfg.oosStart) {
        return "OOS";
    }
    return "IS";
}

function phaseEnabled(timestamp) {
    var phase = phaseFor(timestamp);
    return (phase === "IS" && cfg.tradeInSample) || (phase === "OOS" && cfg.tradeOOS);
}

function emaSeries(values, period) {
    var result = [];
    if (values.length === 0) {
        return result;
    }
    var multiplier = 2 / (period + 1);
    var previous = values[0];
    result.push(previous);
    for (var i = 1; i < values.length; i++) {
        previous = (values[i] - previous) * multiplier + previous;
        result.push(previous);
    }
    return result;
}

function realizedVolatility(values, window) {
    var start = Math.max(1, values.length - window);
    var returns = [];
    var i;
    for (i = start; i < values.length; i++) {
        if (values[i - 1] > 0 && values[i] > 0) {
            returns.push(Math.log(values[i] / values[i - 1]));
        }
    }
    if (returns.length < 2) {
        return 0;
    }
    var mean = 0;
    for (i = 0; i < returns.length; i++) {
        mean += returns[i];
    }
    mean /= returns.length;
    var variance = 0;
    for (i = 0; i < returns.length; i++) {
        variance += Math.pow(returns[i] - mean, 2);
    }
    return Math.sqrt(variance / (returns.length - 1));
}

function signalForCloseHistory() {
    if (state.closes.length < cfg.slowLength || state.closes.length < cfg.slopeBars + 1) {
        return { direction: FLAT, confidence: 0, fast: 0, slow: 0 };
    }
    var fastSeries = emaSeries(state.closes, cfg.fastLength);
    var slowSeries = emaSeries(state.closes, cfg.slowLength);
    var last = state.closes.length - 1;
    var fast = fastSeries[last];
    var slow = slowSeries[last];
    if (!(fast > 0) || !(slow > 0)) {
        return { direction: FLAT, confidence: 0, fast: fast, slow: slow };
    }
    var rising = true;
    var falling = true;
    for (var offset = 0; offset < cfg.slopeBars; offset++) {
        if (last - offset - 1 < 0) {
            return { direction: FLAT, confidence: 0, fast: fast, slow: slow };
        }
        rising = rising && fastSeries[last - offset] > fastSeries[last - offset - 1];
        falling = falling && fastSeries[last - offset] < fastSeries[last - offset - 1];
    }
    var confidence = clamp(Math.abs(fast - slow) / slow * 50, 0, 1);
    if (fast > slow && rising) {
        return { direction: LONG, confidence: confidence, fast: fast, slow: slow };
    }
    if (fast < slow && falling) {
        return { direction: SHORT, confidence: confidence, fast: fast, slow: slow };
    }
    return { direction: FLAT, confidence: 0, fast: fast, slow: slow };
}

function leverageForDecision(confidence) {
    var volatility = realizedVolatility(state.closes, cfg.volatilityLength);
    if (!isFinite(volatility) || volatility < 0 || !isFinite(confidence)) {
        return 1;
    }
    return clamp(1 + confidence * 2 - volatility * cfg.volatilityPenalty, 1, cfg.maxLeverage);
}

function liquidationSafeLeverage(leverage) {
    while (leverage > 1 && (0.9 / leverage) <= cfg.stop) {
        leverage -= 1;
    }
    return clamp(Math.floor(leverage), 1, cfg.maxLeverage);
}

function equityAt(mark) {
    var equity = state.cash;
    if (state.position !== null && mark > 0) {
        equity += state.position.direction * state.position.contracts * cfg.contractBTC * (mark - state.position.entry);
    }
    return equity;
}

function updateDrawdown(mark) {
    var equity = equityAt(mark);
    if (equity > state.peakEquity) {
        state.peakEquity = equity;
    }
    state.maxDrawdown = Math.max(state.maxDrawdown, state.peakEquity - equity);
}

function requestedContractCount(limitPrice, leverage) {
    if (!(limitPrice > 0) || !(leverage >= 1)) {
        return 0;
    }
    return floorToContractStep((cfg.requestedNotionalUSDT * leverage) / (limitPrice * cfg.contractBTC));
}

function contractCount(limitPrice, leverage) {
    var requestedContracts = requestedContractCount(limitPrice, leverage);
    if (requestedContracts < cfg.minContracts) {
        return 0;
    }
    var currentEquity = Math.max(0, equityAt(state.lastClose || limitPrice));
    var budget = currentEquity * cfg.marginAllocation;
    var initialMarginPerContract = (limitPrice * cfg.contractBTC) / leverage;
    if (requestedContracts * initialMarginPerContract <= budget) {
        return requestedContracts;
    }
    return floorToContractStep(budget / initialMarginPerContract);
}

function submitPendingEntry(candle, direction, confidence) {
    var limit = roundPrice(direction === LONG ? candle.Close - cfg.tickSize : candle.Close + cfg.tickSize);
    var leverage = liquidationSafeLeverage(leverageForDecision(confidence));
    var requestedContracts = requestedContractCount(limit, leverage);
    if (requestedContracts < cfg.minContracts) {
        state.skippedSignals++;
        Log("[SKIP] RequestedNotionalUSDT", cfg.requestedNotionalUSDT, "动态杠杆", leverage,
            "所得张数低于 OKX 最小", formatContracts(cfg.minContracts), "price", formatPrice(limit));
        return;
    }
    var contracts = contractCount(limit, leverage);
    if (contracts < cfg.minContracts) {
        state.skippedSignals++;
        Log("[SKIP] 保证金风险预算后的张数低于 OKX 最小", formatContracts(cfg.minContracts),
            "price", formatPrice(limit), "equity", equityAt(candle.Close));
        return;
    }
    if (!nativeSetMarginLevel(leverage)) {
        state.skippedSignals++;
        Log("[NATIVE-ERROR] 无法为入场设置杠杆", leverage + "x");
        return;
    }
    var result;
    try {
        if (direction === LONG) {
            nativeSetDirection("buy");
            result = nativeBuy(limit, contracts);
        } else {
            nativeSetDirection("sell");
            result = nativeSell(limit, contracts);
        }
    } catch (error) {
        Log("[NATIVE-ERROR] 入场提交失败", directionName(direction), String(error));
        return;
    }
    var id = orderIdFrom(result);
    if (id === null) {
        Log("[NATIVE-ERROR] 入场提交未返回有效订单 ID", directionName(direction));
        return;
    }
    state.pending = {
        id: id,
        direction: direction,
        limit: limit,
        contracts: contracts,
        leverage: leverage,
        submittedAt: candle.Time,
        phase: phaseFor(candle.Time),
        cancelRequested: false
    };
    Log("[NATIVE-PENDING] id=" + id, directionName(direction), "limit", formatPrice(limit),
        "contracts", formatContracts(contracts), "lev", leverage + "x", "submittedBar", formatTime(candle.Time));
}

function cancelEntryRemainder(pending, reason) {
    var canceled = nativeCancelOrder(pending.id);
    pending.cancelRequested = true;
    Log("[NATIVE-CANCEL] id=" + pending.id, directionName(pending.direction), reason,
        "result", canceled, "limit", formatPrice(pending.limit));
    return canceled !== false;
}

function createPositionFromEntry(pending, order, candle, dealAmount) {
    var contracts = normalizeContracts(dealAmount);
    if (contracts < cfg.minContracts) {
        return false;
    }
    var entry = orderFillPrice(order, pending.limit);
    var entryFee = contracts * cfg.contractBTC * entry * cfg.makerFee;
    state.cash -= entryFee;
    state.fees += entryFee;
    state.position = {
        direction: pending.direction,
        entry: entry,
        contracts: contracts,
        leverage: pending.leverage,
        openedAt: candle.Time,
        fundingAnchor: candle.Time,
        entryFee: entryFee,
        funding: 0,
        entryPhase: pending.phase,
        entryOrderId: pending.id,
        tpOrder: null
    };
    state.entryFills++;
    if (pending.direction === LONG) {
        state.longEntries++;
    } else {
        state.shortEntries++;
    }
    Log("[NATIVE-FILL] id=" + pending.id, directionName(pending.direction), "entry", formatPrice(entry),
        "contracts", formatContracts(contracts), "lev", pending.leverage + "x",
        "internalMakerFeeDiagnostic", entryFee);
    submitTakeProfit(candle.Close, candle.Time);
    return true;
}

function processPendingEntry(candle) {
    if (state.pending === null) {
        return false;
    }
    var pending = state.pending;
    var order = nativeGetOrder(pending.id);
    var elapsedOneBar = candle.Time > pending.submittedAt;
    if (!order) {
        if (elapsedOneBar && !pending.cancelRequested) {
            cancelEntryRemainder(pending, "一根已完成 K 线后订单状态不可得，保守撤单");
        }
        return false;
    }

    var deal = orderDealAmount(order);
    var amount = orderAmount(order);
    var completelyFilled = isOrderClosed(order) || (amount >= cfg.minContracts && deal >= amount);
    if (!completelyFilled && deal >= cfg.minContracts && !isOrderCanceled(order)) {
        // 部分成交绝不把剩余量当作成交；先撤剩余，再只管理实际 DealAmount。
        if (!cancelEntryRemainder(pending, "检测到部分成交，撤销剩余量")) {
            return false;
        }
        var refreshedPartial = nativeGetOrder(pending.id);
        if (refreshedPartial) {
            order = refreshedPartial;
            deal = orderDealAmount(order);
        }
        completelyFilled = true;
    }

    if (!completelyFilled && !isOrderCanceled(order) && elapsedOneBar) {
        if (!pending.cancelRequested && !cancelEntryRemainder(pending, "超过一根已完成有效 K 线仍未成交")) {
            return false;
        }
        var refreshed = nativeGetOrder(pending.id);
        if (refreshed) {
            order = refreshed;
            deal = orderDealAmount(order);
        }
        if (!refreshed && !pending.cancelRequested) {
            return false;
        }
        completelyFilled = deal >= cfg.minContracts;
    }

    if (completelyFilled || isOrderCanceled(order)) {
        state.pending = null;
        if (deal >= cfg.minContracts) {
            return createPositionFromEntry(pending, order, candle, deal);
        }
        state.entryCancels++;
        Log("[NATIVE-CANCELLED] id=" + pending.id, "status", statusText(order),
            "DealAmount", formatContracts(deal));
    }
    return false;
}

function applyFallbackFunding(until) {
    var position = state.position;
    if (position === null || until <= position.fundingAnchor) {
        return;
    }
    var firstBoundary = (Math.floor(position.fundingAnchor / FUNDING_INTERVAL_MS) + 1) * FUNDING_INTERVAL_MS;
    for (var boundary = firstBoundary; boundary <= until; boundary += FUNDING_INTERVAL_MS) {
        var notional = position.contracts * cfg.contractBTC * position.entry;
        var cashFlow = -position.direction * notional * cfg.fallbackFundingRate;
        state.cash += cashFlow;
        state.funding += cashFlow;
        state.fundingEvents++;
        position.funding += cashFlow;
        position.fundingAnchor = boundary;
        Log("[FUNDING-DIAGNOSTIC]", directionName(position.direction), "time", formatTime(boundary),
            "fallbackRate8h", cfg.fallbackFundingRate, "cashflow", cashFlow);
    }
}

function targetMargin(position, mark, now) {
    var held = now - position.openedAt;
    var margin = cfg.tp;
    if (held >= 6 * HOUR_MS) {
        margin = cfg.tp * 0.67;
    } else if (held >= HOUR_MS) {
        margin = cfg.tp * 0.80;
    }
    margin = Math.max(0.0005, margin);
    var underwater = position.direction === LONG ? mark < position.entry : mark > position.entry;
    return underwater ? 0.0005 : margin;
}

function submitTakeProfit(mark, now) {
    var position = state.position;
    if (position === null || position.tpOrder !== null || position.contracts < cfg.minContracts) {
        return false;
    }
    var target = targetPriceFor(position, mark, now);
    var result;
    try {
        if (position.direction === LONG) {
            // 平多头仓：closebuy = 关闭 buy 方向开的仓位，然后用 Sell 执行
            nativeSetDirection("closebuy");
            result = nativeSell(target, position.contracts);
        } else {
            // 平空头仓：closesell = 关闭 sell 方向开的仓位，然后用 Buy 执行
            nativeSetDirection("closesell");
            result = nativeBuy(target, position.contracts);
        }
    } catch (error) {
        Log("[NATIVE-ERROR] TP 提交失败", directionName(position.direction), String(error));
        return false;
    }
    var id = orderIdFrom(result);
    if (id === null) {
        Log("[NATIVE-ERROR] TP 提交未返回有效订单 ID", directionName(position.direction));
        return false;
    }
    position.tpOrder = { id: id, target: target, contracts: position.contracts, submittedAt: now };
    Log("[NATIVE-TP] id=" + id, directionName(position.direction), "target", formatPrice(target),
        "contracts", formatContracts(position.contracts));
    return true;
}

function settlePosition(exitPrice, exitType, closeTime, requestedAmount, nativeId) {
    var position = state.position;
    if (position === null) {
        return;
    }
    var amount = Math.min(position.contracts, normalizeContracts(requestedAmount));
    if (amount < cfg.minContracts) {
        return;
    }
    var originalContracts = position.contracts;
    var fraction = amount / originalContracts;
    var allocatedEntryFee = position.entryFee * fraction;
    var allocatedFunding = position.funding * fraction;
    var exit = roundPrice(exitPrice);
    var exitFeeRate = exitType === "take-profit-maker" ? cfg.makerFee : cfg.takerFee;
    var exitFee = amount * cfg.contractBTC * exit * exitFeeRate;
    var gross = position.direction * amount * cfg.contractBTC * (exit - position.entry);
    var totalFees = allocatedEntryFee + exitFee;
    var tradePnL = gross - totalFees + allocatedFunding;

    state.cash += gross - exitFee;
    state.fees += exitFee;
    state.trades++;
    if (tradePnL > 0) {
        state.wins++;
    }
    if (exitType === "hard-stop") {
        state.hardStops++;
    } else if (exitType === "take-profit-maker") {
        state.takeProfits++;
    } else {
        state.forcedExits++;
    }
    if (exitType === "approx-liquidation") {
        state.liquidationEvents++;
    }
    var exitPhase = phaseFor(closeTime);
    if (exitPhase === "IS") {
        state.inSampleTrades++;
        state.inSamplePnL += tradePnL;
    } else if (exitPhase === "OOS") {
        state.oosTrades++;
        state.oosPnL += tradePnL;
    }
    Log("[CLOSE-DIAGNOSTIC] nativeId=" + nativeId, exitType, directionName(position.direction),
        "entry", formatPrice(position.entry), "exit", formatPrice(exit), "contracts", formatContracts(amount),
        "gross", gross, "internalFees", totalFees, "funding", allocatedFunding, "pnl", tradePnL);

    var remaining = normalizeContracts(originalContracts - amount);
    if (remaining < cfg.minContracts) {
        state.position = null;
    } else {
        position.contracts = remaining;
        position.entryFee -= allocatedEntryFee;
        position.funding -= allocatedFunding;
        position.tpOrder = null;
    }
}

function processTakeProfit(candle) {
    var position = state.position;
    if (position === null || position.tpOrder === null) {
        return;
    }
    var tp = position.tpOrder;
    var order = nativeGetOrder(tp.id);
    if (!order) {
        return;
    }
    var deal = Math.min(position.contracts, orderDealAmount(order));
    if (deal >= cfg.minContracts) {
        if (!isOrderClosed(order) && !isOrderCanceled(order)) {
            if (nativeCancelOrder(tp.id) === false) {
                return;
            }
            Log("[NATIVE-CANCEL] id=" + tp.id, "TP 部分成交后撤销剩余量");
            var refreshed = nativeGetOrder(tp.id);
            if (refreshed) {
                order = refreshed;
                deal = Math.min(position.contracts, orderDealAmount(order));
            }
        }
        position.tpOrder = null;
        settlePosition(orderFillPrice(order, tp.target), "take-profit-maker", candle.Time, deal, tp.id);
        return;
    }
    if (isOrderCanceled(order) || isOrderClosed(order)) {
        position.tpOrder = null;
        Log("[NATIVE-TP-END] id=" + tp.id, "status", statusText(order), "无可管理成交量");
    }
}

function replaceTakeProfitIfNeeded(candle) {
    var position = state.position;
    if (position === null) {
        return;
    }
    if (position.tpOrder === null) {
        submitTakeProfit(candle.Close, candle.Time);
        return;
    }
    var desired = targetPriceFor(position, candle.Close, candle.Time);
    if (Math.abs(desired - position.tpOrder.target) + cfg.tickSize * 1e-9 < cfg.tickSize) {
        return;
    }
    var old = position.tpOrder;
    if (nativeCancelOrder(old.id) === false) {
        Log("[NATIVE-TP-KEEP] id=" + old.id, "撤单未确认，禁止重复 close 单");
        return;
    }
    Log("[NATIVE-CANCEL] id=" + old.id, "TP 目标调整", formatPrice(old.target), "->", formatPrice(desired));
    var refreshed = nativeGetOrder(old.id);
    if (refreshed) {
        var deal = Math.min(position.contracts, orderDealAmount(refreshed));
        if (deal >= cfg.minContracts) {
            position.tpOrder = null;
            settlePosition(orderFillPrice(refreshed, old.target), "take-profit-maker", candle.Time, deal, old.id);
        } else if (!isOrderCanceled(refreshed) && !isOrderClosed(refreshed)) {
            Log("[NATIVE-TP-KEEP] id=" + old.id, "撤单后状态仍未终结，禁止重复 close 单");
            return;
        } else {
            position.tpOrder = null;
        }
    } else {
        // CancelOrder 已明确成功；即使适配器不再返回已撤订单，也可安全替换。
        position.tpOrder = null;
    }
    if (state.position !== null && state.position.tpOrder === null) {
        submitTakeProfit(candle.Close, candle.Time);
    }
}

function adverseMarketExit(direction, price) {
    return roundPrice(direction === LONG ? price * (1 - cfg.slippage) : price * (1 + cfg.slippage));
}

function decideProtectiveExit(position, candle) {
    var adverseDistance = position.direction === LONG ?
        (position.entry - candle.Low) / position.entry :
        (candle.High - position.entry) / position.entry;
    var liquidationDistance = 0.9 / position.leverage;
    if (adverseDistance >= liquidationDistance) {
        var liquidationBase = position.direction === LONG ? candle.Low : candle.High;
        return { type: "approx-liquidation", modeledPrice: adverseMarketExit(position.direction, liquidationBase) };
    }
    var stop = position.direction === LONG ? position.entry * (1 - cfg.stop) : position.entry * (1 + cfg.stop);
    var stopHit = position.direction === LONG ? candle.Low <= stop : candle.High >= stop;
    if (stopHit) {
        var stopBase = position.direction === LONG ? Math.min(stop, candle.Low) : Math.max(stop, candle.High);
        return { type: "hard-stop", modeledPrice: adverseMarketExit(position.direction, stopBase) };
    }
    if (candle.Time - position.openedAt >= cfg.maxHoldMs || (cfg.endTime > 0 && candle.Time >= cfg.endTime)) {
        return { type: "forced-market", modeledPrice: adverseMarketExit(position.direction, candle.Close) };
    }
    return null;
}

function cancelTakeProfitBeforeMarketExit(candle) {
    var position = state.position;
    if (position === null || position.tpOrder === null) {
        return true;
    }
    var tp = position.tpOrder;
    var before = nativeGetOrder(tp.id);
    if (before && orderDealAmount(before) >= cfg.minContracts) {
        processTakeProfit(candle);
        if (state.position === null) {
            return false;
        }
        position = state.position;
        if (position.tpOrder === null) {
            return true;
        }
        tp = position.tpOrder;
    }
    if (nativeCancelOrder(tp.id) === false) {
        Log("[NATIVE-EXIT-BLOCKED] TP id=" + tp.id, "撤单未确认，为防止重复平仓暂不发送市价 close");
        return false;
    }
    Log("[NATIVE-CANCEL] id=" + tp.id, "保护性退出前撤销 TP");
    var after = nativeGetOrder(tp.id);
    if (after) {
        var deal = Math.min(position.contracts, orderDealAmount(after));
        if (deal >= cfg.minContracts) {
            position.tpOrder = null;
            settlePosition(orderFillPrice(after, tp.target), "take-profit-maker", candle.Time, deal, tp.id);
            if (state.position === null) {
                return false;
            }
            position = state.position;
        } else if (!isOrderCanceled(after) && !isOrderClosed(after)) {
            Log("[NATIVE-EXIT-BLOCKED] TP id=" + tp.id, "撤单后仍为活动状态");
            return false;
        }
    }
    position.tpOrder = null;
    return true;
}

function submitNativeMarketExit(decision, candle) {
    if (state.position === null || !cancelTakeProfitBeforeMarketExit(candle) || state.position === null) {
        return;
    }
    var position = state.position;
    var contracts = position.contracts;
    var result;
    try {
        if (position.direction === LONG) {
            // 平多头仓：closebuy = 关闭 buy 方向开的仓位，然后用 Sell 执行市价退出
            nativeSetDirection("closebuy");
            result = nativeSell(-1, contracts);
        } else {
            // 平空头仓：closesell = 关闭 sell 方向开的仓位，然后用 Buy 执行市价退出
            nativeSetDirection("closesell");
            result = nativeBuy(-1, contracts);
        }
    } catch (error) {
        Log("[NATIVE-ERROR] 保护性市价平仓提交失败", decision.type, String(error));
        return;
    }
    var id = orderIdFrom(result);
    if (id === null) {
        Log("[NATIVE-ERROR] 保护性市价平仓未返回有效订单 ID", decision.type);
        return;
    }
    state.lastNativeExit = { id: id, type: decision.type, contracts: contracts, submittedAt: candle.Time };
    Log("[NATIVE-MARKET-EXIT] id=" + id, decision.type, directionName(position.direction),
        "price=-1", "contracts", formatContracts(contracts));

    var order = nativeGetOrder(id);
    var actualDeal = order ? Math.min(contracts, orderDealAmount(order)) : 0;
    if (actualDeal >= cfg.minContracts) {
        settlePosition(orderFillPrice(order, decision.modeledPrice), decision.type, candle.Time, actualDeal, id);
    }
    if (state.position !== null) {
        // FMZ 市价单未能立即返回完整成交详情：平台仍持有原生订单；内部诊断用保守价格结算剩余量。
        Log("[NATIVE-MARKET-EXIT-PENDING] id=" + id,
            "即时成交详情不完整；FMZ 平台继续撮合，内部诊断使用保守 modeledExit", formatPrice(decision.modeledPrice));
        settlePosition(decision.modeledPrice, decision.type, candle.Time, state.position.contracts, id);
    }
}

function processCandle(candle) {
    if (!candle || !(candle.Time > 0) || !(candle.Close > 0) || !(candle.High > 0) || !(candle.Low > 0)) {
        return;
    }
    if (state.lastProcessedTime !== 0 && candle.Time <= state.lastProcessedTime) {
        return;
    }
    state.lastProcessedTime = candle.Time;
    if (state.firstProcessedTime === 0) {
        state.firstProcessedTime = candle.Time;
    }
    state.lastClose = candle.Close;

    var openedThisCandle = processPendingEntry(candle);
    if (state.position !== null && !openedThisCandle) {
        applyFallbackFunding(candle.Time);
        processTakeProfit(candle);
        if (state.position !== null) {
            var exitDecision = decideProtectiveExit(state.position, candle);
            if (exitDecision !== null) {
                submitNativeMarketExit(exitDecision, candle);
            } else {
                replaceTakeProfitIfNeeded(candle);
            }
        }
    }

    updateDrawdown(candle.Close);
    state.closes.push(candle.Close);
    var historyLimit = Math.max(cfg.slowLength + cfg.slopeBars + 5, cfg.volatilityLength + 5, 64);
    if (state.closes.length > historyLimit) {
        state.closes.shift();
    }
    if (state.closes.length < cfg.slowLength) {
        if (!state.warmupLogged) {
            Log("[WARMUP] 已收集", state.closes.length + "/" + cfg.slowLength, "个样本；预热期间不建仓");
            state.warmupLogged = true;
        }
        return;
    }
    if (state.position !== null || state.pending !== null ||
        (cfg.endTime > 0 && candle.Time >= cfg.endTime) || !phaseEnabled(candle.Time)) {
        return;
    }
    var signal = signalForCloseHistory();
    if (signal.direction !== FLAT) {
        state.rawSignals++;
    }
    if (signal.direction === FLAT || signal.confidence < cfg.minConfidence) {
        return;
    }
    state.qualifiedSignals++;
    submitPendingEntry(candle, signal.direction, signal.confidence);
}

function processClosedRecords(records) {
    if (!records || records.length < 2) {
        return;
    }
    for (var i = 0; i < records.length - 1; i++) {
        processCandle(records[i]);
    }
}

function logFinalTotals() {
    if (state === null || cfg === null || state.finalLogged) {
        return;
    }
    state.finalLogged = true;
    var finalEquity = equityAt(state.lastClose);
    var pnl = finalEquity - cfg.initialEquity;
    var winRate = state.trades > 0 ? state.wins / state.trades * 100 : 0;
    var reportStart = cfg.inSampleStart > 0 ? Math.max(state.firstProcessedTime, cfg.inSampleStart) : state.firstProcessedTime;
    var reportEnd = cfg.endTime > 0 ? Math.min(state.lastProcessedTime, cfg.endTime) : state.lastProcessedTime;
    var spanDays = reportEnd > reportStart ? (reportEnd - reportStart) / (24 * HOUR_MS) : 0;
    var tradesPerDay = spanDays > 0 ? state.trades / spanDays : 0;
    var maxDrawdownPct = state.peakEquity > 0 ? state.maxDrawdown / state.peakEquity * 100 : 0;
    Log("===== BTC-USDT-SWAP 自适应做市 FMZ 原生订单回测汇总 =====");
    if (cfg.dateMode === "AUTO") {
        Log("日期分区", "AUTO：FMZ 实际已处理范围", formatTime(state.firstProcessedTime), "至",
            formatTime(state.lastProcessedTime), "；全部按 IS 处理");
    } else {
        Log("日期分区", "MANUAL", "InSampleStart", formatTime(cfg.inSampleStart),
            "OOSStart", formatTime(cfg.oosStart), "EndTime", formatTime(cfg.endTime),
            "实际已处理", formatTime(state.firstProcessedTime), "至", formatTime(state.lastProcessedTime));
    }
    Log("成交", "trades", state.trades, "longEntries", state.longEntries, "shortEntries", state.shortEntries,
        "nativeEntryFills", state.entryFills, "nativeEntryCancels", state.entryCancels, "skipped", state.skippedSignals);
    Log("内部诊断收益（非 FMZ 平台账户）", "initialEquity", cfg.initialEquity, "diagnosticEquity", finalEquity,
        "PnL", pnl, "winRate%", winRate, "maxDrawdown", state.maxDrawdown,
        "maxDrawdown%", maxDrawdownPct, "trades/day", tradesPerDay);
    Log("内部诊断成本（FMZ 平台另行核算）", "maker/taker%", cfg.makerFee * 100 + "/" + cfg.takerFee * 100,
        "fees", state.fees, "funding", state.funding, "fallbackFundingEvents", state.fundingEvents);
    Log("退出", "TP", state.takeProfits, "hardStops", state.hardStops,
        "forced", state.forcedExits, "approxLiquidations", state.liquidationEvents);
    Log("分区结果", "IS trades/PnL", state.inSampleTrades + "/" + state.inSamplePnL,
        "OOS trades/PnL", state.oosTrades + "/" + state.oosPnL);
    if (state.position !== null) {
        Log("[ONEXIT-POSITION-REMAINS] 回测已停止，不再创建新平仓单；剩余", directionName(state.position.direction),
            "contracts", formatContracts(state.position.contracts), "entry", formatPrice(state.position.entry));
    }
}

function startupSizingSelfCheck() {
    var price = 100000;
    var requestedNotionalUSDT = 50;
    var contractBTC = 0.01;
    var oneX = floorToContractStep((requestedNotionalUSDT * 1) / (price * contractBTC));
    var threeX = floorToContractStep((requestedNotionalUSDT * 3) / (price * contractBTC));
    var belowMinimum = floorToContractStep(0.009);
    if (oneX !== 0.05 || threeX !== 0.15 || belowMinimum !== 0) {
        throw "合约步长启动自检失败：1x=" + oneX + "，3x=" + threeX + "，belowMinimum=" + belowMinimum;
    }
    return { oneX: oneX, threeX: threeX, belowMinimum: belowMinimum };
}

function main() {
    // 强制放在 main 第一条可执行语句，且早于 SetContractType、SetDirection、Buy/Sell 等任何订单路径。
    requireVirtualBacktest();
    cfg = buildConfig();
    var sizingSelfCheck = startupSizingSelfCheck();
    state = newState(cfg.initialEquity);
    nativeSetContractType();
    Log("[START] BTC-USDT-SWAP FMZ 原生期货回测", "warmup", cfg.slowLength,
        "TP%", cfg.tp * 100, "stop%", cfg.stop * 100, "maxLeverage", cfg.maxLeverage,
        "requestedNotionalUSDT", cfg.requestedNotionalUSDT, "fundingFallback8h%", cfg.fallbackFundingRate * 100);
    Log("[START][FMZ-FEE] 请在 FMZ 回测 UI 设置 maker=0.02%、taker=0.05%，滑点按需要设置；平台账户统计以 UI 撮合模型为准，脚本内费用仅为诊断。" );
    Log("[START][SAFETY] IsVirtual() === true 已确认；所有原生订单 API 仍会逐次复检，实盘环境将立即拒绝。" );
    Log("[START][SELF-CHECK] price=100000 requestedNotionalUSDT=50", "1x contracts", formatContracts(sizingSelfCheck.oneX),
        "3x contracts", formatContracts(sizingSelfCheck.threeX), "below 0.01 contracts", formatContracts(sizingSelfCheck.belowMinimum));
    if (cfg.dateMode === "AUTO") {
        Log("[START][DATE] AUTO：允许 FMZ 所选范围内全部已完成 K 线交易。" );
    } else {
        Log("[START][DATE] MANUAL：InSampleStart", formatTime(cfg.inSampleStart),
            "OOSStart", formatTime(cfg.oosStart), "EndTime", formatTime(cfg.endTime));
    }
    Log("[START][SIZE] ctVal", cfg.contractBTC, "BTC/张，lot/min", formatContracts(cfg.contractLotSize),
        "张；目标张数按价格、RequestedNotionalUSDT 和动态 1–3x 杠杆计算。" );
    // 启动数据自检：确认合约绑定与 GetRecords 是否真的返回数据。
    var probe = exchange.GetRecords();
    Log("[START][DATA-PROBE] contractType", exchange.GetContractType(), "currency", exchange.GetCurrency(),
        "records", probe === null ? "null" : (probe.length + " bars"),
        "firstTime", probe && probe.length > 0 ? formatTime(probe[0].Time) : "n/a",
        "lastClose", probe && probe.length > 0 ? probe[probe.length - 1].Close : "n/a");
    while (true) {
        var records = exchange.GetRecords();
        processClosedRecords(records);
        Sleep(Math.max(1, Math.floor(numberValue(PollMilliseconds, 1000))));
    }
}

function cancelPendingAtExit() {
    if (state === null) {
        return;
    }
    if (state.pending !== null) {
        var pending = state.pending;
        var result = nativeCancelOrder(pending.id);
        Log("[ONEXIT-CANCEL] entry id=" + pending.id, "result", result);
        state.pending = null;
    }
    if (state.position !== null && state.position.tpOrder !== null) {
        var tp = state.position.tpOrder;
        var tpResult = nativeCancelOrder(tp.id);
        Log("[ONEXIT-CANCEL] TP id=" + tp.id, "result", tpResult);
        state.position.tpOrder = null;
    }
}

// FMZ 回测停止后只撤活动挂单并输出结果；绝不在 onexit 创建新 close 单。
function onexit() {
    if (virtualBacktestConfirmed && state !== null) {
        cancelPendingAtExit();
    }
    logFinalTotals();
}
