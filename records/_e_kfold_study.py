# -*- coding: utf-8 -*-
"""E 策略(跨所价差回归)K 折研究.

口径: src="rest" 且 (无 module 或 module=="e") 且 exit!="unfilled".
按时间排序切 6 个等样本量片做 K 折; 过检标准 = >=5/6 片"好组-坏组"净中位差同号.
python3.6 兼容(无 fromisoformat / 无 f-string 调试语法).
"""
import json
import time
import calendar
from collections import Counter

LEDGER = '/root/lingxi/records/okx_directional/particle_ledger.jsonl'
NFOLD = 6


def parse_ts(s):
    # "2026-07-07T14:16:26.214053218Z" -> epoch seconds (float, 精度到秒即可)
    base = s.split('.')[0]
    return calendar.timegm(time.strptime(base, '%Y-%m-%dT%H:%M:%S'))


def load_rows():
    rows = []
    with open(LEDGER) as f:
        for line in f:
            try:
                r = json.loads(line)
            except ValueError:
                continue
            if r.get('src') != 'rest':
                continue
            m = r.get('module')
            if m is not None and m != 'e':
                continue
            if r.get('exit') == 'unfilled':
                continue
            r['_t0'] = parse_ts(r['t0'])
            rows.append(r)
    rows.sort(key=lambda r: r['_t0'])
    for i, r in enumerate(rows):
        r['_fold'] = min(i * NFOLD // len(rows), NFOLD - 1)
    return rows


def median(xs):
    xs = sorted(xs)
    n = len(xs)
    if n == 0:
        return None
    if n % 2:
        return xs[n // 2]
    return 0.5 * (xs[n // 2 - 1] + xs[n // 2])


def pf(nets):
    wins = sum(x for x in nets if x > 0)
    loss = -sum(x for x in nets if x < 0)
    if loss <= 0:
        return float('inf') if wins > 0 else 0.0
    return wins / loss


def fmt(x, w=8, p=1):
    if x is None:
        return ' ' * (w - 2) + '--'
    if x == float('inf'):
        return ' ' * (w - 3) + 'inf'
    return ('%' + str(w) + '.' + str(p) + 'f') % x


def group_stats(rows):
    nets = [r['net_bps'] for r in rows]
    if not nets:
        return '   n=  0'
    winr = 100.0 * sum(1 for x in nets if x > 0) / len(nets)
    c = Counter(r['symbol'] for r in rows)
    top_sym, top_n = c.most_common(1)[0]
    conc = 100.0 * top_n / len(rows)
    warn = '  !单币%s=%.0f%%' % (top_sym, conc) if conc > 30 else ''
    return 'n=%3d 净中位=%s PF=%s 胜率=%s%%%s' % (
        len(nets), fmt(median(nets), 7), fmt(pf(nets), 6, 2), fmt(winr, 5, 0), warn)


def kfold_compare(rows, name, story, good_fn, bad_fn):
    """good_fn/bad_fn: row -> True/False/None(缺失). 返回过检与否."""
    good = [r for r in rows if good_fn(r) is True]
    bad = [r for r in rows if bad_fn(r) is True]
    print('')
    print('### 特征: %s' % name)
    print('    对手故事: %s' % story)
    print('    好组  %s' % group_stats(good))
    print('    坏组  %s' % group_stats(bad))
    signs = []
    detail = []
    for k in range(NFOLD):
        g = [r['net_bps'] for r in good if r['_fold'] == k]
        b = [r['net_bps'] for r in bad if r['_fold'] == k]
        mg, mb = median(g), median(b)
        if mg is None or mb is None:
            detail.append('片%d: 缺样本(g=%d,b=%d)' % (k, len(g), len(b)))
            signs.append(None)
        else:
            d = mg - mb
            detail.append('片%d: 好%s 坏%s 差%s' % (k, fmt(mg, 7), fmt(mb, 7), fmt(d, 7)))
            signs.append(1 if d > 0 else (-1 if d < 0 else 0))
    print('    ' + ' | '.join(detail))
    pos = sum(1 for s in signs if s == 1)
    neg = sum(1 for s in signs if s == -1)
    passed = (pos >= 5) or (neg >= 5)
    direction = '好>坏' if pos > neg else '好<坏(方向反!)'
    print('    K折: 同号 +%d/-%d/缺%d -> %s  [%s]' % (
        pos, neg, signs.count(None), '过检' if passed else '不过检', direction))
    return passed, pos, neg, good, bad


def combo_eval(rows, cond, label):
    sel = [r for r in rows if cond(r)]
    nets = [r['net_bps'] for r in sel]
    if len(sel) < 25:
        return None
    med = median(nets)
    p = pf(nets)
    fold_meds = []
    ok_folds = 0
    have_folds = 0
    for k in range(NFOLD):
        g = [r['net_bps'] for r in sel if r['_fold'] == k]
        m = median(g)
        fold_meds.append((k, len(g), m))
        if m is not None:
            have_folds += 1
            if m > 0:
                ok_folds += 1
    c = Counter(r['symbol'] for r in sel)
    top_sym, top_n = c.most_common(1)[0]
    conc = 100.0 * top_n / len(sel)
    return dict(label=label, n=len(sel), med=med, pf=p, ok_folds=ok_folds,
                have_folds=have_folds, fold_meds=fold_meds,
                top_sym=top_sym, conc=conc, nets=nets)


def main():
    rows = load_rows()
    print('=' * 100)
    print('E 策略 K 折研究  样本 n=%d  t0: %s .. %s' % (
        len(rows), rows[0]['t0'][:19], rows[-1]['t0'][:19]))
    for k in range(NFOLD):
        fr = [r for r in rows if r['_fold'] == k]
        print('  片%d: n=%3d  %s .. %s  %s' % (
            k, len(fr), fr[0]['t0'][5:16], fr[-1]['t0'][5:16], group_stats(fr)))
    print('  全表: %s' % group_stats(rows))

    # ---------------- 1. 亏损结构分解(按 exit) ----------------
    print('')
    print('=' * 100)
    print('一、亏损结构分解(按 exit)')
    total_loss = -sum(r['net_bps'] for r in rows if r['net_bps'] < 0)
    total_win = sum(r['net_bps'] for r in rows if r['net_bps'] > 0)
    print('  全表: 总赢=%.0fbps 总亏=%.0fbps 净=%.0fbps' % (total_win, total_loss, total_win - total_loss))
    for ex in ('converged', 'max_hold', 'disaster'):
        sub = [r for r in rows if r['exit'] == ex]
        loss = -sum(r['net_bps'] for r in sub if r['net_bps'] < 0)
        win = sum(r['net_bps'] for r in sub if r['net_bps'] > 0)
        print('  exit=%-9s %s  贡献亏损=%.0fbps(占总亏%.0f%%) 贡献盈利=%.0fbps' % (
            ex, group_stats(sub), loss, 100.0 * loss / total_loss if total_loss else 0, win))
    # converged 亏损单细分: gross<0 = 锚砸下来闭合(OKX 自己也跌了); 0<=gross<12 = 毛利被成本吃掉
    conv = [r for r in rows if r['exit'] == 'converged']
    conv_lose = [r for r in conv if r['net_bps'] < 0]
    anchor_closed = [r for r in conv_lose if r.get('gross_bps', 0) < 0]
    cost_eaten = [r for r in conv_lose if r.get('gross_bps', 0) >= 0]
    print('  converged 亏损单 n=%d (占 converged %.0f%%):' % (
        len(conv_lose), 100.0 * len(conv_lose) / len(conv) if conv else 0))
    print('    锚砸下来闭合(gross<0, OKX 价自己也在跌): n=%d (%.0f%%) 净中位=%s' % (
        len(anchor_closed), 100.0 * len(anchor_closed) / len(conv_lose) if conv_lose else 0,
        fmt(median([r['net_bps'] for r in anchor_closed]), 7)))
    print('    毛利为正但被 12bps 成本吃掉(0<=gross<12): n=%d (%.0f%%) 净中位=%s' % (
        len(cost_eaten), 100.0 * len(cost_eaten) / len(conv_lose) if conv_lose else 0,
        fmt(median([r['net_bps'] for r in cost_eaten]), 7)))
    # peak 结构: 有多少单曾经浮盈过再回吐
    for ex in ('converged', 'max_hold', 'disaster'):
        sub = [r for r in rows if r['exit'] == ex and r['net_bps'] < 0]
        had_peak = [r for r in sub if r.get('peak_bps', 0) >= 12]
        print('  exit=%-9s 亏损单中曾浮盈>=12bps(够付成本)的: %d/%d' % (ex, len(had_peak), len(sub)))

    # ---------------- 2. 单特征 K 折 ----------------
    print('')
    print('=' * 100)
    print('二、单特征 K 折(过检=至少5/6片好-坏净中位差同号)')

    results = {}

    def dep(r):  # 深度 = |gap_det|
        return abs(r['gap_det_bps'])

    results['dep60_100'] = kfold_compare(
        rows, '① 探测深度 60-100 vs 40-60',
        '裂口越深越可能是真错价(浅口=噪声+成本占比高); 但太深可能是真消息.',
        lambda r: 60 <= dep(r) < 100, lambda r: dep(r) < 60)
    results['dep100'] = kfold_compare(
        rows, '① 探测深度 100+ vs 40-60',
        '超深裂口=灾难性事件概率高, 可能不是错价而是真跌.',
        lambda r: dep(r) >= 100, lambda r: dep(r) < 60)

    def resid(r):
        return r.get('gap_rest_bps')

    results['rest_band'] = kfold_compare(
        rows, '② REST残余带 [-40,-20] vs 更深(<-40)',
        '复核时已回一半=本地噪声在自愈, 接着回; 复核仍很深=真错位或真跌.',
        lambda r: resid(r) is not None and -40 <= resid(r) <= -20,
        lambda r: resid(r) is not None and resid(r) < -40)
    results['rest_deep'] = kfold_compare(
        rows, '② REST残余仍深(<-40) vs 已大半闭合(>-20)',
        '残余深=还有肉; 已闭合大半=追尾巴付全额成本.',
        lambda r: resid(r) is not None and resid(r) < -40,
        lambda r: resid(r) is not None and resid(r) > -20)

    sp_med = median([r['spread_bps'] for r in rows])
    print('')
    print('    (spread_bps 全表中位=%.1f, 用它做高低分界)' % sp_med)
    results['spread'] = kfold_compare(
        rows, '③ 盘口点差 窄(<中位%.0f) vs 宽' % sp_med,
        '点差宽=流动性差, mid 价差是幻觉(实际成交价吃掉边际); 窄点差裂口更真.',
        lambda r: r['spread_bps'] < sp_med, lambda r: r['spread_bps'] >= sp_med)

    def calm(r):
        return r.get('fuel_5m', 0) < 40 and r.get('breadth_30m', 0) > -15

    results['calm'] = kfold_compare(
        rows, '④ 平静海州(燃料<40 且 广度>-15) vs 风暴',
        '平静时裂口=局部错价会回; 风暴时裂口=全场瀑布, 锚随后跟跌.',
        calm, lambda r: not calm(r))

    bit_story = {
        1: ('新鲜度过', '新鲜裂口=对手还没反应完; 旧裂口=市场已定价.'),
        2: ('燃料平静', '同平静海逻辑: 无爆仓燃料时裂口是噪声.'),
        4: ('广度OK', '全场没在普跌时, 单币裂口更像局部错价.'),
        8: ('深度40-60', '浅带样本(同①).'),
        16: ('深度>=60', '深带样本(同①).'),
        32: ('非股票perp', '股票perp锚定质量差(现货休市/定价源弱), 价差回归假设不成立.'),
        64: ('惯犯币', '惯犯=历史上常开裂口的币, 裂口是它的常态而非错价.'),
        128: ('派班', '被调度选中(执行流内部标记).'),
        256: ('残余带[-40,-20]', '同②残余带.'),
    }
    for bit in (1, 2, 4, 32, 64, 256):
        nm, st = bit_story[bit]
        results['bit%d' % bit] = kfold_compare(
            rows, '⑤ bit%d %s=1 vs =0' % (bit, nm), st,
            lambda r, b=bit: bool(r.get('bits', 0) & b),
            lambda r, b=bit: not (r.get('bits', 0) & b))

    # 路径三件套(仅在字段可得的行上)
    def age(r):
        return r.get('gap_age_sec')

    results['age'] = kfold_compare(
        rows, '⑥ 生龄 gap_age<=5s(新裂口) vs >5s(老裂口)',
        '新裂口=刚砸出来的恐慌盘, 回弹快; 老裂口=挂了很久没人套=有原因的价差.',
        lambda r: age(r) is not None and age(r) <= 5,
        lambda r: age(r) is not None and age(r) > 5)

    def vel15(r):
        return r.get('gap_vel15_bps')

    results['vel15'] = kfold_compare(
        rows, '⑥ vel15<0(还在扩大) vs vel15>=0(已在收敛) 【已知毒桶复核】',
        '已在收敛才进=残余肉少+我们是最后一个接盘的; 还在扩大=接的是下落中的刀但均值回归空间大.',
        lambda r: vel15(r) is not None and vel15(r) < 0,
        lambda r: vel15(r) is not None and vel15(r) >= 0)

    def okx_led(r):
        a = r.get('gap_okx_ret15_bps')
        b = r.get('gap_anchor_ret15_bps')
        if a is None or b is None:
            return None
        return abs(a) > abs(b)

    results['attrib'] = kfold_compare(
        rows, '⑥ 归因: OKX主导(|okx_ret15|>|anchor_ret15|) vs 锚主导 【重点】',
        'OKX自己砸出来的裂口=本地恐慌盘/胖手指, 会回(买它); 锚拉出来的裂口=对面先行, 锚可能回头砸下来闭合=我们接飞刀.',
        lambda r: okx_led(r) is True,
        lambda r: okx_led(r) is False)

    results['stock'] = kfold_compare(
        rows, '⑦ 币(bit32=1非股票) vs 股票perp',
        '股票perp在美股休市时锚是死价, 裂口闭合全靠 OKX 单边漂移=纯方向赌.',
        lambda r: bool(r.get('bits', 0) & 32),
        lambda r: not (r.get('bits', 0) & 32))

    results['repeat'] = kfold_compare(
        rows, '⑧ 非惯犯(bit64=0) vs 惯犯',
        '惯犯币常开裂口=结构性价差(资金费/流动性折价), 不闭合或慢闭合.',
        lambda r: not (r.get('bits', 0) & 64),
        lambda r: bool(r.get('bits', 0) & 64))

    print('')
    print('-' * 100)
    print('过检清单:')
    for key, (passed, pos, neg, g, b) in results.items():
        tag = '过检' if passed else '  --'
        print('  [%s] %-12s 同号+%d/-%d  好组%s | 坏组%s' % (
            tag, key, pos, neg, group_stats(g)[:40], group_stats(b)[:40]))

    # ---------------- 3. 最优组合(<=2 特征) ----------------
    print('')
    print('=' * 100)
    print('三、最优组合搜索(<=2 特征, n>=25, 目标: 净中位>0 且 PF>=1.3 且 >=5/6 片中位>0)')

    preds = [
        ('深度40-60', lambda r: dep(r) < 60),
        ('深度60-100', lambda r: 60 <= dep(r) < 100),
        ('深度>=60', lambda r: dep(r) >= 60),
        ('深度>=100', lambda r: dep(r) >= 100),
        ('残余[-40,-20]', lambda r: resid(r) is not None and -40 <= resid(r) <= -20),
        ('残余<-40', lambda r: resid(r) is not None and resid(r) < -40),
        ('点差窄', lambda r: r['spread_bps'] < sp_med),
        ('点差宽', lambda r: r['spread_bps'] >= sp_med),
        ('平静海', calm),
        ('风暴', lambda r: not calm(r)),
        ('新鲜bit1', lambda r: bool(r.get('bits', 0) & 1)),
        ('币非股票', lambda r: bool(r.get('bits', 0) & 32)),
        ('股票perp', lambda r: not (r.get('bits', 0) & 32)),
        ('非惯犯', lambda r: not (r.get('bits', 0) & 64)),
        ('惯犯', lambda r: bool(r.get('bits', 0) & 64)),
        ('生龄<=5s', lambda r: age(r) is not None and age(r) <= 5),
        ('生龄>5s', lambda r: age(r) is not None and age(r) > 5),
        ('vel15扩大中', lambda r: vel15(r) is not None and vel15(r) < 0),
        ('vel15收敛中', lambda r: vel15(r) is not None and vel15(r) >= 0),
        ('OKX主导', lambda r: okx_led(r) is True),
        ('锚主导', lambda r: okx_led(r) is False),
    ]

    combos = []
    for i, (n1, f1) in enumerate(preds):
        c = combo_eval(rows, f1, n1)
        if c:
            combos.append(c)
        for j in range(i + 1, len(preds)):
            n2, f2 = preds[j]
            c = combo_eval(rows, lambda r, a=f1, b=f2: a(r) and b(r), n1 + ' & ' + n2)
            if c:
                combos.append(c)

    def score(c):
        return (c['ok_folds'], c['med'] if c['med'] is not None else -1e9, min(c['pf'], 9.9))

    combos.sort(key=score, reverse=True)
    winners = [c for c in combos if c['med'] is not None and c['med'] > 0
               and c['pf'] >= 1.3 and c['ok_folds'] >= 5]
    print('  搜索了 %d 个组合(单+双), 满足全部标准的: %d 个' % (len(combos), len(winners)))
    show = winners if winners else combos[:8]
    hdr = '合格组合' if winners else '最接近的前8名(均不合格)'
    print('  %s:' % hdr)
    for c in show[:8]:
        folds = ' '.join('片%d(n=%d)%s' % (k, n, fmt(m, 6)) for k, n, m in c['fold_meds'])
        warn = ' !单币%s=%.0f%%' % (c['top_sym'], c['conc']) if c['conc'] > 30 else ''
        print('    %-28s n=%3d 净中位=%s PF=%s 正片=%d/%d%s' % (
            c['label'], c['n'], fmt(c['med'], 7), fmt(c['pf'], 5, 2),
            c['ok_folds'], c['have_folds'], warn))
        print('        跨片: %s' % folds)

    # ---------------- 4. 出场结构反事实(峰值回吐) ----------------
    print('')
    print('=' * 100)
    print('四、出场结构反事实: 亏在入场还是亏在出场?')
    gross = [r['gross_bps'] for r in rows]
    print('  全表 gross(未扣成本)中位=%.1f, gross>0 占 %.0f%%, gross>12(够付成本) 占 %.0f%%' % (
        median(gross), 100.0 * sum(1 for x in gross if x > 0) / len(gross),
        100.0 * sum(1 for x in gross if x > 12) / len(gross)))
    peaks = [r.get('peak_bps', 0) for r in rows]
    print('  peak_bps 中位=%.1f; 持仓中曾浮盈>=20bps 的单: %d/%d (%.0f%%)' % (
        median(peaks), sum(1 for x in peaks if x >= 20), len(peaks),
        100.0 * sum(1 for x in peaks if x >= 20) / len(peaks)))
    cf = []
    for r in rows:
        pk = r.get('peak_bps', 0)
        if pk >= 20:
            cf.append(max(r['net_bps'], pk - 8 - 12))
        else:
            cf.append(r['net_bps'])
    print('  粗反事实(peak>=20 后挂 8bps 尾随, 其余原版出场):')
    print('    原版:   净中位=%s PF=%s 总净=%.0fbps' % (
        fmt(median([r['net_bps'] for r in rows]), 6), fmt(pf([r['net_bps'] for r in rows]), 5, 2),
        sum(r['net_bps'] for r in rows)))
    print('    尾随:   净中位=%s PF=%s 总净=%.0fbps' % (fmt(median(cf), 6), fmt(pf(cf), 5, 2), sum(cf)))
    trail = [r for r in rows if r.get('trail_exit_bps') is not None]
    if trail:
        base = [r['net_bps'] for r in trail]
        tcf = [r['trail_exit_bps'] if r.get('trail_fired') else r['net_bps'] for r in trail]
        print('  账本实测尾随影子(trail_exit_bps, 仅 n=%d 行有记录):' % len(trail))
        print('    原版: 净中位=%s PF=%s 总净=%.0f | 尾随: 净中位=%s PF=%s 总净=%.0f' % (
            fmt(median(base), 6), fmt(pf(base), 5, 2), sum(base),
            fmt(median(tcf), 6), fmt(pf(tcf), 5, 2), sum(tcf)))
    hb = [r for r in rows if r.get('burst_during_hold')]
    nb = [r for r in rows if not r.get('burst_during_hold')]
    print('  持仓期间遇 burst: n=%d 净中位=%s | 未遇: n=%d 净中位=%s (burst 是出场期风险, 入场时不可知)' % (
        len(hb), fmt(median([r['net_bps'] for r in hb]), 6),
        len(nb), fmt(median([r['net_bps'] for r in nb]), 6)))

    print('')
    print('=' * 100)
    print('(脚本结束; 结论见研究员报告)')


if __name__ == '__main__':
    main()
