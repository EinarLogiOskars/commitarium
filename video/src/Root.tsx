import { Composition } from "remotion";
import { END, ReadmeLoop } from "./ReadmeLoop";

// Videos about Commitarium, drawn with its own styles and components so they
// re-render to match whenever the UI changes.
export function Root() {
  return (
    <Composition
      id="ReadmeLoop"
      component={ReadmeLoop}
      durationInFrames={END}
      fps={30}
      width={1280}
      height={800}
    />
  );
}
