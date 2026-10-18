import { Composition } from "remotion";
import { ARCHITECTURE_HEIGHT, ARCHITECTURE_WIDTH, Architecture } from "./Architecture";
import { END, ReadmeLoop } from "./ReadmeLoop";

// Videos about Commitarium, drawn with its own styles and components so they
// re-render to match whenever the UI changes.
export function Root() {
  return (
    <>
      <Composition
        id="ReadmeLoop"
        component={ReadmeLoop}
        durationInFrames={END}
        fps={30}
        width={1280}
        height={800}
      />
      {(["light", "dark"] as const).map((theme) => (
        <Composition
          key={theme}
          id={`Architecture-${theme}`}
          component={Architecture}
          defaultProps={{ theme }}
          durationInFrames={1}
          fps={30}
          width={ARCHITECTURE_WIDTH}
          height={ARCHITECTURE_HEIGHT}
        />
      ))}
    </>
  );
}
