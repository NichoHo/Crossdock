import { NextResponse } from "next/server";
import { endSession } from "@/lib/session";

export async function POST() {
  await endSession();
  return new NextResponse(null, { status: 303, headers: { Location: "/login" } });
}
