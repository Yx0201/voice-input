package main

// salvageTail / lastPunctCut 的用例来自 2026-09-25 真实用户日志中的吞字与
// 乱码案例(app.log"定稿与已提交不一致"三连 + "\xe3" 烂字节),回归保护。

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestLastPunctCut(t *testing.T) {
	cases := []struct {
		name    string
		partial string
		want    string
		wantOK  bool
	}{
		{
			name:    "事故复现:中文句号是 3 字节,切分必须含完整 rune(曾经切出 \\xe3)",
			partial: "哇哦，这看起来效果非常nice啊。",
			want:    "哇哦，这看起来效果非常nice啊。",
			wantOK:  true,
		},
		{
			name:    "顿号同为 3 字节",
			partial: "苹果、香蕉、梨",
			want:    "苹果、香蕉、",
			wantOK:  true,
		},
		{
			name:    "ASCII 标点(旧实现在这里恰好正确)",
			partial: "hello, world. next",
			want:    "hello, world.",
			wantOK:  true,
		},
		{
			name:    "无标点",
			partial: "没有标点的一段话",
			want:    "",
			wantOK:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := lastPunctCut(tc.partial)
			if ok != tc.wantOK || got != tc.want {
				t.Fatalf("got (%q, %v), want (%q, %v)", got, ok, tc.want, tc.wantOK)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("切分结果含非法 UTF-8: %q", got)
			}
		})
	}
}

// 事故不变量:任何 partial 的切分结果都必须是合法 UTF-8 且以标点结尾。
func TestLastPunctCutNeverSplitsRunes(t *testing.T) {
	partials := []string{
		"短。",
		"一句话带逗号，后面还有内容继续说",
		"嗯、啊、呃就是说这个话题其实挺有意思的大家都在讨论呢",
		"mixed ascii text. with 中文 words! 和标点?",
	}
	for _, p := range partials {
		got, ok := lastPunctCut(p)
		if ok && (!utf8.ValidString(got) || !strings.HasSuffix(got, "。") && !lastPunctSuffixOK(got)) {
			t.Fatalf("切分 %q → %q 不是干净的标点边界", p, got)
		}
	}
}

func lastPunctSuffixOK(s string) bool {
	r := []rune(s)
	if len(r) == 0 {
		return false
	}
	last := r[len(r)-1]
	for _, p := range streamPuncts {
		if last == p {
			return true
		}
	}
	return false
}

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
			name:      "事故复现 09-25:续说时云端把句尾句号改成逗号,带标点锚点全失配",
			committed: "那么如果是流式输出加上我们的润色，这个时候如果用户说的话是非常完整的一段话，没有特别多的特别明显的语义拆分，就会完整的等到120个字的阈值。",
			final:     "那么如果是流式输出加上我们的润色，这个时候如果用户说的话是非常完整的一段话，没有特别多的特别明显的语义拆分，就会完整的等到120个字的阈值，实际它的效果也就是完整输出的效果嘛，这个我是能理解的。",
			wantTail:  "，实际它的效果也就是完整输出的效果嘛，这个我是能理解的。",
			wantOK:    true,
		},
		{
			name:      "去掉末字符锚点也找不到 → 放弃",
			committed: "说了一句完全不同的话。",
			final:     "毫无交集的另起炉灶",
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
