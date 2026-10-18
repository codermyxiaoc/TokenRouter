export type CanvasColorTheme = "light" | "dark";
export type CanvasBackgroundMode = "dots" | "lines" | "blank";

export const canvasThemes = {
    light: {
        canvas: {
            background: "#FAFAFA",
            dot: "rgba(68,64,60,.28)",
            line: "rgba(113,113,122,.1)",
            selectionStroke: "#0B8FD8",
            selectionFill: "rgba(28,25,23,.06)",
        },
        node: {
            label: "#52525B",
            fill: "#F4F4F5",
            panel: "#FFFFFF",
            stroke: "#E4E4E7",
            activeStroke: "#0B8FD8",
            placeholder: "#A1A1AA",
            text: "#27272A",
            muted: "#71717A",
            faint: "#A1A1AA",
        },
        toolbar: {
            panel: "rgba(255,255,255,.96)",
            border: "#E4E4E7",
            item: "#52525B",
            itemHover: "#F4F4F5",
            activeBg: "#F4F4F5",
            activeText: "#27272A",
        },
    },
    dark: {
        canvas: {
            background: "#18181B",
            dot: "rgba(245,245,244,.24)",
            line: "rgba(244,244,245,.06)",
            selectionStroke: "#4DD4F6",
            selectionFill: "rgba(250,250,249,.10)",
        },
        node: {
            label: "#D4D4D8",
            fill: "#27272A",
            panel: "#202023",
            stroke: "#3F3F46",
            activeStroke: "#4DD4F6",
            placeholder: "#A1A1AA",
            text: "#F4F4F5",
            muted: "#D4D4D8",
            faint: "#71717A",
        },
        toolbar: {
            panel: "rgba(32,32,35,.96)",
            border: "#3F3F46",
            item: "#D4D4D8",
            itemHover: "#27272A",
            activeBg: "#303034",
            activeText: "#F4F4F5",
        },
    },
} as const;

export type CanvasTheme = (typeof canvasThemes)[CanvasColorTheme];
