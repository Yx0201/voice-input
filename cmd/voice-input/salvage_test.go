package main

// salvageTail 的用例全部来自 2026-09-25 真实用户日志中的吞字案例
// (app.log "定稿与已提交不一致" 三连),回归保护:同样的回改不再丢尾巴。

import "testing"

func TestSalvageTail(t *testing.T) {
	cases := []struct {
		name      string
		committed string
		final     string
		wantTail  string
		wantOK    bool
	}{
		{
			name: "云端定稿插入逗号,尾巴是句末两字",
			committed: "我现在想做一些测试，因为我在刚才的语音转文字的情况下会发现最后有几个字还是会被",
			final:     "我现在想做一些测试，因为我在刚才的语音转文字的情况下，会发现最后有几个字还是会被吞掉。",
			wantTail:  "吞掉。",
			wantOK:    true,
		},
		{
			name: "云端定稿删掉中途逗号,尾巴是长尾段",
			committed: "这个问题能够排查一下吗？如果最后几个字被吞掉，还是挺",
			final:     "这个问题能够排查一下吗？如果最后几个字被吞掉还是挺影响体验的。",
			wantTail:  "影响体验的。",
			wantOK:    true,
		},
		{
			name: "云端定稿纠正同音字(解断→截断),尾巴只剩句号",
			committed: "在某一个听写时刻他突然就停止了，后面的字就全都解断了一样",
			final:     "在某一个听写时刻他突然就停止了，后面的字就全都截断了一样。",
			wantTail:  "。",
			wantOK:    true,
		},
		{
			name:      "定稿正常续写(HasPrefix 主路径,不走补救)",
			committed: "今天天气不错",
			final:     "今天天气不错，适合出门。",
			wantTail:  "，适合出门。",
			wantOK:    true,
		},
		{
			name:      "定稿被整体重写,无锚点可对齐 → 放弃",
			committed: "甲乙丙丁",
			final:     "完全不同的另一句话。",
			wantTail:  "",
			wantOK:    false,
		},
		{
			name:      "锚点之后没有新内容 → 和解但尾巴为空",
			committed: "说了一段话还是会被",
			final:     "说了一段话还是会被。",
			wantTail:  "。",
			wantOK:    true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tail, ok := salvageTail(tc.committed, tc.final)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if tail != tc.wantTail {
				t.Fatalf("tail = %q, want %q", tail, tc.wantTail)
			}
		})
	}
}
