from fastapi import FastAPI
import uuid, torchaudio
from audiocraft.models import MusicGen

app = FastAPI(title="SCARLIX MusicGen")
OUTPUT_DIR = "/app/output"
model = None

def get_model():
    global model
    if model is None:
        model = MusicGen.get_pretrained("facebook/musicgen-medium")
    return model

@app.get("/health")
def health():
    return {"status": "ok", "service": "musicgen"}

@app.post("/generate")
def generate(prompt: str, duration: int = 30):
    m = get_model()
    m.set_generation_params(duration=duration)
    wav = m.generate([prompt])
    out = f"{OUTPUT_DIR}/{uuid.uuid4().hex}.wav"
    torchaudio.save(out, wav[0].cpu(), sample_rate=32000)
    return {"file": out, "status": "generated"}
