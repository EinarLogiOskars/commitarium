import { Composition } from "remotion";
import { ARCHITECTURE_HEIGHT, ARCHITECTURE_WIDTH, Architecture } from "./Architecture";
import { END, PlantPalScreenshot, ReadmeLoop, THUMBNAIL_HEIGHT, THUMBNAIL_WIDTH, VideoThumbnail } from "./ReadmeLoop";

// Videos about Commitarium, drawn with its own styles and components so they
// re-render to match whenever the UI changes.
export function Root() {
  return (
    <>
      {(["light", "dark"] as const).map((theme) => (
        <Composition
          key={theme}
          id={`ReadmeLoop-${theme}`}
          component={ReadmeLoop}
          defaultProps={{ theme }}
          durationInFrames={END}
          fps={30}
          width={1280}
          height={800}
        />
      ))}
      <Composition
        id="PlantPalScreenshot"
        component={PlantPalScreenshot}
        durationInFrames={1}
        fps={30}
        width={1280}
        height={800}
      />
      <Composition
        id="VideoThumbnail"
        component={VideoThumbnail}
        durationInFrames={1}
        fps={30}
        width={THUMBNAIL_WIDTH}
        height={THUMBNAIL_HEIGHT}
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
