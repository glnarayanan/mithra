import { Audio } from '@remotion/media';
import { useCallback, useEffect, useState } from 'react';
import {
  AbsoluteFill,
  Composition,
  Easing,
  Img,
  interpolate,
  Series,
  staticFile,
  useCurrentFrame,
  useDelayRender,
  useVideoConfig,
} from 'remotion';

const FPS = 30;

const scenes = [
  { id: 'proposition', durationInFrames: 17 * FPS, audio: 'voiceover/scene-01.wav' },
  { id: 'overview', durationInFrames: 18 * FPS, audio: 'voiceover/scene-02.wav' },
  { id: 'week', durationInFrames: 51 * FPS, audio: 'voiceover/scene-03.wav' },
  { id: 'privacy', durationInFrames: 24 * FPS, audio: 'voiceover/scene-04.wav' },
  { id: 'build', durationInFrames: 38 * FPS, audio: 'voiceover/scene-05.wav' },
  { id: 'close', durationInFrames: 12 * FPS, audio: 'voiceover/scene-06.wav' },
] as const;

const durationInFrames = scenes.reduce((total, scene) => total + scene.durationInFrames, 0);

const colors = {
  ink: '#121514',
  muted: '#5b605f',
  paper: '#f5f4f0',
  mint: '#d4faec',
  green: '#057a57',
  line: '#dedfda',
  white: '#ffffff',
};

// Replace these checked-in placeholders with the final clean product captures
// before release. Keeping the stable keys makes that asset swap a file-only change.
const screens = {
  familyBrief: 'screens/family-brief.png',
  finance: 'screens/finance.png',
  health: 'screens/health.png',
  planning: 'screens/planning.png',
  weekOwner: 'screens/week-owner.png',
  weekPartner: 'screens/week-partner.png',
};

type Caption = {
  text: string;
  startMs: number;
  endMs: number;
  timestampMs: number | null;
  confidence: number | null;
};

const Screen: React.FC<{
  src: string;
  label: string;
  objectPosition?: string;
  compact?: boolean;
  scroll?: boolean;
}> = ({ src, label, objectPosition = 'center top', compact = false, scroll = false }) => {
  const frame = useCurrentFrame();

  return (
    <div
      aria-label={label}
      style={{
        position: 'relative',
        height: '100%',
        overflow: 'hidden',
        border: `1px solid ${colors.line}`,
        borderRadius: compact ? 22 : 30,
        background: colors.white,
        boxShadow: '0 28px 80px rgba(18, 21, 20, 0.16)',
        opacity: interpolate(frame, [0, 18], [0, 1], {
          extrapolateRight: 'clamp',
          easing: Easing.bezier(0.16, 1, 0.3, 1),
        }),
        scale: interpolate(frame, [0, 150], [1.025, 1], {
          extrapolateRight: 'clamp',
          easing: Easing.bezier(0.16, 1, 0.3, 1),
        }),
      }}
    >
      <Img
        src={staticFile(src)}
        style={{
          width: '100%',
          height: '100%',
          objectFit: 'cover',
          objectPosition: scroll
            ? `center ${interpolate(frame, [0, 1500], [0, 60], {
                extrapolateLeft: 'clamp',
                extrapolateRight: 'clamp',
                easing: Easing.bezier(0.16, 1, 0.3, 1),
              })}%`
            : objectPosition,
        }}
      />
    </div>
  );
};

const SceneShell: React.FC<{
  eyebrow: string;
  title: string;
  note: string;
  audio: string;
  children: React.ReactNode;
}> = ({ eyebrow, title, note, audio, children }) => {
  const frame = useCurrentFrame();

  return (
    <AbsoluteFill
      style={{
        background: colors.paper,
        color: colors.ink,
        fontFamily: 'Arial, Helvetica, sans-serif',
      }}
    >
      <Audio src={staticFile(audio)} />
      <div
        style={{
          position: 'absolute',
          width: 640,
          height: 640,
          right: -210,
          top: -330,
          borderRadius: '50%',
          background: colors.mint,
          opacity: 0.7,
        }}
      />
      <div
        style={{
          position: 'absolute',
          top: 68,
          left: 94,
          right: 94,
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'flex-end',
          gap: 56,
          opacity: interpolate(frame, [0, 18], [0, 1], {
            extrapolateRight: 'clamp',
            easing: Easing.bezier(0.16, 1, 0.3, 1),
          }),
          translate: interpolate(frame, [0, 22], ['0px 22px', '0px 0px'], {
            extrapolateRight: 'clamp',
            easing: Easing.bezier(0.16, 1, 0.3, 1),
          }),
        }}
      >
        <div>
          <div
            style={{
              color: colors.green,
              fontSize: 27,
              fontWeight: 800,
              letterSpacing: 0.5,
              textTransform: 'uppercase',
            }}
          >
            {eyebrow}
          </div>
          <div
            style={{
              maxWidth: 1120,
              marginTop: 8,
              fontSize: 68,
              fontWeight: 800,
              letterSpacing: -3.4,
              lineHeight: 1.02,
            }}
          >
            {title}
          </div>
        </div>
        <div
          style={{
            maxWidth: 460,
            paddingBottom: 5,
            color: colors.muted,
            fontSize: 28,
            lineHeight: 1.25,
            textAlign: 'right',
          }}
        >
          {note}
        </div>
      </div>
      <div style={{ position: 'absolute', top: 250, right: 94, bottom: 130, left: 94 }}>{children}</div>
    </AbsoluteFill>
  );
};

const CaptionTrack: React.FC = () => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const [captions, setCaptions] = useState<Caption[] | null>(null);
  const { delayRender, continueRender, cancelRender } = useDelayRender();
  const [handle] = useState(() => delayRender('Load video captions'));

  const loadCaptions = useCallback(async () => {
    try {
      const response = await fetch(staticFile('captions.json'));
      if (!response.ok) {
        throw new Error(`Could not load captions: ${response.status}`);
      }
      setCaptions((await response.json()) as Caption[]);
      continueRender(handle);
    } catch (error) {
      cancelRender(error);
    }
  }, [cancelRender, continueRender, handle]);

  useEffect(() => {
    void loadCaptions();
  }, [loadCaptions]);

  if (!captions) {
    return null;
  }

  const now = (frame / fps) * 1000;
  const caption = captions.find((item) => item.startMs <= now && item.endMs > now);
  if (!caption) {
    return null;
  }

  return (
    <div
      style={{
        position: 'absolute',
        right: 220,
        bottom: 34,
        left: 220,
        display: 'flex',
        justifyContent: 'center',
        pointerEvents: 'none',
      }}
    >
      <div
        style={{
          maxWidth: 1380,
          padding: '14px 24px',
          borderRadius: 16,
          background: 'rgba(18, 21, 20, 0.88)',
          color: colors.white,
          fontFamily: 'Arial, Helvetica, sans-serif',
          fontSize: 30,
          fontWeight: 700,
          lineHeight: 1.25,
          textAlign: 'center',
        }}
      >
        {caption.text}
      </div>
    </div>
  );
};

const PropositionScene: React.FC = () => (
  <SceneShell
    eyebrow="Mithra"
    title="One clear weekly plan for a busy household"
    note="Finance, health and plans in one calm view."
    audio={scenes[0].audio}
  >
    <Screen src={screens.familyBrief} label="Family Brief" objectPosition="center 8%" />
  </SceneShell>
);

const OverviewScene: React.FC = () => {
  const cards = [
    { label: 'Family Brief', src: screens.familyBrief },
    { label: 'Finance', src: screens.finance },
    { label: 'Health', src: screens.health },
    { label: 'Planning', src: screens.planning },
  ];

  return (
    <SceneShell
      eyebrow="One household view"
      title="Shared facts, with room for private ones"
      note="The household sees what it needs. Each adult keeps control of the rest."
      audio={scenes[1].audio}
    >
      <div style={{ display: 'grid', height: '100%', gridTemplateColumns: 'repeat(2, 1fr)', gap: 24 }}>
        {cards.map((card) => (
          <div key={card.label} style={{ position: 'relative', minHeight: 0 }}>
            <div
              style={{
                position: 'absolute',
                top: 0,
                left: 0,
                zIndex: 1,
                margin: 18,
                padding: '9px 14px',
                borderRadius: 999,
                background: colors.ink,
                color: colors.white,
                fontSize: 22,
                fontWeight: 800,
              }}
            >
              {card.label}
            </div>
            <Screen src={card.src} label={card.label} compact />
          </div>
        ))}
      </div>
    </SceneShell>
  );
};

const WeekScene: React.FC = () => (
  <SceneShell
    eyebrow="Week in Review"
    title="Start with what matters this week"
    note="Grounded facts first. Clear priorities next."
    audio={scenes[2].audio}
  >
    <Screen src={screens.weekOwner} label="Owner Week in Review" scroll />
  </SceneShell>
);

const PrivacyScene: React.FC = () => (
  <SceneShell
    eyebrow="Shared by choice"
    title="Private records stay private"
    note="The owner and partner views share household facts, not private health or finance data."
    audio={scenes[3].audio}
  >
    <div style={{ display: 'grid', height: '100%', gridTemplateColumns: '1fr 1fr', gap: 36 }}>
      <div style={{ minHeight: 0 }}>
        <div style={{ marginBottom: 14, color: colors.green, fontSize: 27, fontWeight: 800 }}>Owner view</div>
        <div style={{ height: 'calc(100% - 50px)' }}>
          <Screen src={screens.weekOwner} label="Owner private review" objectPosition="center bottom" compact />
        </div>
      </div>
      <div style={{ minHeight: 0 }}>
        <div style={{ marginBottom: 14, color: colors.green, fontSize: 27, fontWeight: 800 }}>Partner view</div>
        <div style={{ height: 'calc(100% - 50px)' }}>
          <Screen src={screens.weekPartner} label="Partner private review" objectPosition="center bottom" compact />
        </div>
      </div>
    </div>
  </SceneShell>
);

const BuildScene: React.FC = () => {
  const frame = useCurrentFrame();
  const points = [
    ['Codex', 'Repository tracing, typed Week processing, tests and browser checks'],
    ['GPT-5.6', 'Product critique, UX choices, prioritisation, prompt design and review'],
    ['Builder', 'Final scope, privacy, safety and product decisions'],
  ];

  return (
    <SceneShell
      eyebrow="Built with Codex + GPT-5.6"
      title="AI supported the build. Facts stay grounded."
      note="The product owner kept the final say on every shipped decision."
      audio={scenes[4].audio}
    >
      <div style={{ display: 'flex', height: '100%', flexDirection: 'column', justifyContent: 'center', gap: 22 }}>
        {points.map(([title, description], index) => (
          <div
            key={title}
            style={{
              display: 'grid',
              gridTemplateColumns: '290px 1fr',
              alignItems: 'center',
              gap: 34,
              minHeight: 140,
              padding: '30px 40px',
              border: `1px solid ${index === 0 ? colors.ink : colors.line}`,
              borderRadius: 26,
              background: index === 0 ? colors.ink : colors.white,
              color: index === 0 ? colors.white : colors.ink,
              opacity: interpolate(frame, [index * 44, index * 44 + 28], [0, 1], {
                extrapolateRight: 'clamp',
                easing: Easing.bezier(0.16, 1, 0.3, 1),
              }),
              translate: interpolate(frame, [index * 44, index * 44 + 28], ['-28px 0px', '0px 0px'], {
                extrapolateRight: 'clamp',
                easing: Easing.bezier(0.16, 1, 0.3, 1),
              }),
            }}
          >
            <div style={{ fontSize: 42, fontWeight: 800 }}>{title}</div>
            <div style={{ color: index === 0 ? '#d7dedb' : colors.muted, fontSize: 31, lineHeight: 1.25 }}>{description}</div>
          </div>
        ))}
      </div>
    </SceneShell>
  );
};

const CloseScene: React.FC = () => (
  <SceneShell
    eyebrow="Mithra"
    title="See what changed. Prepare for what is next."
    note="Hosted demo and public repository available."
    audio={scenes[5].audio}
  >
    <Screen src={screens.weekOwner} label="Mithra Week in Review" objectPosition="center top" />
  </SceneShell>
);

const MithraDemo: React.FC = () => (
  <AbsoluteFill>
    <Series>
      <Series.Sequence name="Problem and proposition" durationInFrames={scenes[0].durationInFrames}>
        <PropositionScene />
      </Series.Sequence>
      <Series.Sequence name="Product overview" durationInFrames={scenes[1].durationInFrames}>
        <OverviewScene />
      </Series.Sequence>
      <Series.Sequence name="Core Week in Review demo" durationInFrames={scenes[2].durationInFrames}>
        <WeekScene />
      </Series.Sequence>
      <Series.Sequence name="Privacy and health safety" durationInFrames={scenes[3].durationInFrames}>
        <PrivacyScene />
      </Series.Sequence>
      <Series.Sequence name="Codex and GPT-5.6 collaboration" durationInFrames={scenes[4].durationInFrames}>
        <BuildScene />
      </Series.Sequence>
      <Series.Sequence name="Closing" durationInFrames={scenes[5].durationInFrames}>
        <CloseScene />
      </Series.Sequence>
    </Series>
    <CaptionTrack />
  </AbsoluteFill>
);

export const MyComposition: React.FC = () => (
  <Composition
    id="MithraBuildWeekDemo"
    component={MithraDemo}
    durationInFrames={durationInFrames}
    fps={FPS}
    width={1920}
    height={1080}
    calculateMetadata={() => ({ durationInFrames })}
  />
);
