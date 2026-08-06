import { Playground } from "@/components/Playground";
import { Shell } from "@/components/Shell";

export default function PlaygroundPage() {
  return (
    <Shell>
      <div style={{ maxWidth: 880 }}>
        <Playground />
      </div>
    </Shell>
  );
}
