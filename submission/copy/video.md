# Video asset and render notes

The final render is
[`submission/video/mithra-build-week-demo.mp4`](../video/mithra-build-week-demo.mp4).
It is a 160-second, 1920 × 1080 H.264/AAC MP4 with spoken macOS narration and
burned-in captions. It uses no music, stock media, credentials, or personal
data.

The Remotion source lives in `tools/demo-video`. From that directory:

```bash
npm run lint
npx remotion render src/index.ts MithraBuildWeekDemo ../../submission/video/mithra-build-week-demo.mp4
```

The screenshots in `tools/demo-video/public/screens/` are the final clean Owner
and Partner captures. The privacy scene uses the two distinct Week in Review
views.

The narration and captions cover the problem, product overview, Week in
Review, private health safety, Codex and GPT-5.6 collaboration, and the hosted
demo. The checked file is 160 seconds, 1920 × 1080, H.264 with AAC audio. Upload
it to YouTube as public or unlisted; do not upload it from the repository.
