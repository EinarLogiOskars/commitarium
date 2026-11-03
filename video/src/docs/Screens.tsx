import { Composition } from "remotion";
import type { ComponentType } from "react";
import { AppScreen } from "./AppScreen";
import { AppWindow } from "../../../desktop/src/views/AppWindow";
import { BOOKS, PLANTS, RECIPES } from "./demo";
import { Screen, type Theme } from "./Screen";

// The docs' screenshots: each the app's real view over the demo world.
const SCREENS: { id: string; width?: number; height?: number; steps?: string[]; view: ComponentType }[] = [
  { id: "apps", view: () => <AppScreen /> },
  { id: "agents", steps: ["Agents"], view: () => <AppScreen /> },
  { id: "agents-mayka", steps: ["Agents", "Mayka"], view: () => <AppScreen /> },
  { id: "settings-general", steps: ["Settings"], view: () => <AppScreen /> },
  { id: "new-app", steps: ["New app"], view: () => <AppScreen /> },
  { id: "settings-server", steps: ["Settings", "Server"], view: () => <AppScreen server /> },
  { id: "backups", steps: ["Settings", "Backup"], view: () => <AppScreen server /> },
  { id: "dev-chat", view: () => <AppScreen dev area="projects" project={PLANTS} /> },
  {
    id: "work-order",
    steps: ["All work", "Log waterings and keep history"],
    view: () => <AppScreen dev area="projects" project={PLANTS} />,
  },
  { id: "vision", steps: ["Open the vision"], view: () => <AppScreen dev area="projects" project={PLANTS} /> },
  { id: "app-building", view: () => <AppWindow projectId={BOOKS} /> },
  { id: "first-look", view: () => <AppScreen dev area="projects" project={RECIPES} /> },
  { id: "sync-card", steps: ["scroll:Sync"], view: () => <AppScreen dev area="projects" project={RECIPES} /> },
  { id: "place-switcher", view: () => <AppScreen server /> },
  { id: "connect-server", steps: ["choose:Connect a server…"], view: () => <AppScreen /> },
  { id: "import-project", steps: ["All", "Import project…"], view: () => <AppScreen dev area="projects" /> },
  { id: "deploy", steps: ["Deployment page"], view: () => <AppScreen server dev area="projects" project={PLANTS} /> },
  {
    id: "online-access",
    steps: ["Deployment page", "scroll:Deployment"],
    view: () => <AppScreen server signIn dev area="projects" project={PLANTS} />,
  },
  {
    id: "linked-repository",
    steps: ["scroll:Linked repository"],
    view: () => <AppScreen server dev area="projects" project={PLANTS} />,
  },
  {
    id: "sign-in-settings",
    steps: ["Settings", "Server", "scroll:Company sign-in"],
    view: () => <AppScreen server />,
  },
  {
    id: "sign-in-mayka",
    view: () => <AppScreen server dev area="projects" project={PLANTS} chat="chat_signin" />,
  },
];

// Each screen's component is made once: one made during render would be a
// new component each time, remounting the view and restarting its clicks.
const COMPOSITIONS = SCREENS.flatMap(({ id, view: View, steps, width = 1280, height = 800 }) =>
  (["light", "dark"] as Theme[]).map((theme) => ({
    id: `docs-${id}-${theme}`,
    width,
    height,
    component: () => (
      <Screen theme={theme} steps={steps}>
        <View />
      </Screen>
    ),
  })),
);

export function DocsScreens() {
  return (
    <>
      {COMPOSITIONS.map(({ id, width, height, component }) => (
        <Composition key={id} id={id} component={component} durationInFrames={1} fps={30} width={width} height={height} />
      ))}
    </>
  );
}
