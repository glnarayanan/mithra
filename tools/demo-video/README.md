# Mithra Build Week demo video

This Remotion project produces a 2:40, 1920 × 1080 narrated MP4 for OpenAI
Build Week. It uses real Mithra screenshots, local macOS Samantha narration,
and burned-in JSON captions. It uses no music, stock media, or network media.

```bash
npm run lint
npx remotion render src/index.ts MithraBuildWeekDemo ../../submission/video/mithra-build-week-demo.mp4
```

The narration source is in `narration/`. Regenerate it on macOS with:

```bash
for i in 01 02 03 04 05 06; do
  say -v Samantha -r 150 -f "narration/scene-$i.txt" -o "public/voiceover/scene-$i.aiff"
  afconvert -f WAVE -d LEI16 "public/voiceover/scene-$i.aiff" "public/voiceover/scene-$i.wav"
done
```

The PNG files in `public/screens/` are clean final captures. Keep their file
names when making a later render:

- `week-owner.png`: Owner Week in Review
- `week-partner.png`: Partner Week in Review
- `family-brief.png`: shared Family Brief
- `finance.png`, `health.png`, `planning.png`: supporting product views

The privacy scene reads those two files directly. Do not show credentials,
browser chrome, local URLs, developer tools, or personal data.

The final MP4 must remain below three minutes. The narration states what Codex
and GPT-5.6 did during the build and keeps the builder's final decision-making
clear.
